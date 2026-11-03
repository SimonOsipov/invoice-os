// prodid_vars_workflow_test.go pins that the workflows read the production ids from Actions variables and stop when one is missing.
package main

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var prodIDWorkflows = []string{"deploy-prod.yml", "rollback-prod.yml", "nightly.yml", "dev-env.yml", "dev-env-sweeper.yml", "dev-env-teardown.yml"}

const (
	wifVar        = "${{ vars.GCP_WIF_PROVIDER }}"
	portalVar     = "${{ vars.HUBSPOT_PORTAL_ID }}"
	teardownCheck = `[ -n "$GCP_WIF_PROVIDER" ] || { echo "::error::GCP_WIF_PROVIDER is not set. Add it under Settings → Secrets and variables → Actions → Variables."; exit 1; }`
)

// checkRoots lists the root GCP jobs. Teardown inlines its check: it checks out the PR head.
type checkRoot struct {
	wf, job string
	names   []string
	inline  bool
}

var checkRoots = []checkRoot{
	{"deploy-prod.yml", "await-ci", []string{"GCP_WIF_PROVIDER", "HUBSPOT_PORTAL_ID"}, false},
	{"dev-env.yml", "invariants", []string{"GCP_WIF_PROVIDER"}, false},
	{"dev-env.yml", "prepare-env", []string{"GCP_WIF_PROVIDER", "HUBSPOT_PORTAL_ID"}, false},
	{"dev-env.yml", "build", []string{"GCP_WIF_PROVIDER", "HUBSPOT_PORTAL_ID"}, false},
	{"rollback-prod.yml", "rollback", []string{"GCP_WIF_PROVIDER"}, false},
	{"nightly.yml", "prod-check", []string{"GCP_WIF_PROVIDER"}, false},
	{"dev-env-sweeper.yml", "sweep", []string{"GCP_WIF_PROVIDER"}, false},
	{"dev-env-teardown.yml", "teardown", []string{"GCP_WIF_PROVIDER"}, true},
}

var wifLine = regexp.MustCompile(`(?m)^\s*workload_identity_provider:\s*(.*?)\s*$`)

func isAuth(s workflowStep) bool {
	return strings.HasPrefix(s.keys["uses"], "google-github-actions/auth@")
}

func authJobs(yml string) []workflowJob {
	var out []workflowJob
	for _, j := range workflowJobsOf(yml) {
		if slices.ContainsFunc(j.steps(), isAuth) {
			out = append(out, j)
		}
	}
	return out
}

// wifProblems reports an auth value that is neither allowed expression, and a file without an auth step.
func wifProblems(wf, yml string) []string {
	var out []string
	for _, m := range wifLine.FindAllStringSubmatch(yml, -1) {
		if m[1] != "${{ env.WIF_PROVIDER }}" && m[1] != wifVar {
			out = append(out, fmt.Sprintf("%s: workload_identity_provider is %q", wf, m[1]))
		}
	}
	if len(authJobs(yml)) == 0 {
		out = append(out, wf+": no auth step")
	}
	return out
}

// topEnv returns the workflow-level env: map.
func topEnv(yml string) map[string]string {
	env := map[string]string{}
	in := false
	for _, l := range stripHashComments(strings.Split(yml, "\n")) {
		switch {
		case l == "env:":
			in = true
		case in && strings.HasPrefix(l, "  "):
			if k, v, ok := strings.Cut(strings.TrimSpace(l), ":"); ok {
				env[k] = strings.TrimSpace(v)
			}
		case in && strings.TrimSpace(l) != "":
			return env
		}
	}
	return env
}

func envProblems(wf, yml string, want map[string]string) []string {
	var out []string
	env := topEnv(yml)
	for k, v := range want {
		if env[k] != v {
			out = append(out, fmt.Sprintf("%s: env %s is %q, want %q", wf, k, env[k], v))
		}
	}
	return out
}

// checkProblems pins one root job's check step: placement, names, mapping, no continue-on-error.
func checkProblems(yml string, root int) []string {
	r := checkRoots[root]
	label := r.wf + " " + r.job
	var job *workflowJob
	for _, j := range workflowJobsOf(yml) {
		if j.name == r.job {
			job = &j
		}
	}
	if job == nil {
		return []string{label + ": no such job"}
	}
	steps := job.steps()
	if r.inline {
		return inlineProblems(label, steps)
	}
	auth := slices.IndexFunc(steps, isAuth)
	if auth < 0 {
		auth = len(steps) // await-ci has no auth step: every later job needs it
	}
	idx := slices.IndexFunc(steps, func(s workflowStep) bool {
		return strings.Contains(s.keys["run"], "scripts/ci/require-vars.sh")
	})
	if idx < 0 {
		return []string{label + ": no require-vars.sh step"}
	}
	var out []string
	s := steps[idx]
	if idx > auth {
		out = append(out, label+": the check runs after auth")
	}
	if co := slices.IndexFunc(steps, func(x workflowStep) bool { return x.keys["uses"] == "actions/checkout@v4" }); co < 0 || idx < co {
		out = append(out, label+": the check runs before checkout")
	}
	if want := "bash scripts/ci/require-vars.sh " + strings.Join(r.names, " "); s.keys["run"] != want {
		out = append(out, fmt.Sprintf("%s: run is %q, want %q", label, s.keys["run"], want))
	}
	for _, n := range r.names {
		if s.env[n] != "${{ vars."+n+" }}" {
			out = append(out, fmt.Sprintf("%s: env %s is %q", label, n, s.env[n]))
		}
	}
	if len(s.env) != len(r.names) {
		out = append(out, label+": env maps other names")
	}
	if _, ok := s.keys["continue-on-error"]; ok {
		out = append(out, label+": the check has continue-on-error")
	}
	return out
}

func inlineProblems(label string, steps []workflowStep) []string {
	s := steps[0]
	var out []string
	if s.keys["uses"] != "" {
		out = append(out, label+": the first step is not the inline check")
	}
	run := s.keys["run"]
	if strings.Contains(run, "scripts/ci/") {
		out = append(out, label+": the inline check names scripts/ci/")
	}
	if !strings.Contains(run, `[ -n "$GCP_WIF_PROVIDER" ]`) || !strings.Contains(run, teardownCheck) {
		out = append(out, label+": the inline check lacks the test or the copy")
	}
	if s.env["GCP_WIF_PROVIDER"] != wifVar {
		out = append(out, label+": the inline check does not map GCP_WIF_PROVIDER from vars")
	}
	if _, ok := s.keys["continue-on-error"]; ok {
		out = append(out, label+": the check has continue-on-error")
	}
	return out
}

// authReachProblems reports each auth job that is not a root job and reaches none through needs.
func authReachProblems(wf, yml string) []string {
	jobs := map[string]workflowJob{}
	for _, j := range workflowJobsOf(yml) {
		jobs[j.name] = j
	}
	isRoot := func(name string) bool {
		return slices.ContainsFunc(checkRoots, func(r checkRoot) bool {
			return r.wf == wf && r.job == name
		})
	}
	var reaches func(name string, seen map[string]bool) bool
	reaches = func(name string, seen map[string]bool) bool {
		if isRoot(name) {
			return true
		}
		if seen[name] {
			return false
		}
		seen[name] = true
		for _, n := range needsOf(jobs[name]) {
			if reaches(n, seen) {
				return true
			}
		}
		return false
	}
	var out []string
	for _, j := range authJobs(yml) {
		if !reaches(j.name, map[string]bool{}) {
			out = append(out, fmt.Sprintf("%s %s: has auth and reaches no checked root job", wf, j.name))
		}
	}
	return out
}

var (
	portalDigitArg = regexp.MustCompile(`VITE_HUBSPOT_PORTAL_ID=\d`)
	checkBlock     = regexp.MustCompile(`(?m)^      - name: Check the Actions variables\n(?:        .*\n)+`)
)

func spaBuildProblems(wf, yml string) []string {
	out := envProblems(wf, yml, map[string]string{"HUBSPOT_PORTAL_ID": portalVar})
	found := false
	for _, j := range workflowJobsOf(yml) {
		for _, s := range j.steps() {
			if s.keys["name"] != "Build and push" {
				continue
			}
			found = true
			run := s.keys["run"]
			if !strings.Contains(run, `"VITE_HUBSPOT_PORTAL_ID=$HUBSPOT_PORTAL_ID"`) {
				out = append(out, wf+": Build and push lacks the portal build argument")
			}
			if portalDigitArg.MatchString(run) {
				out = append(out, wf+": Build and push holds a literal portal id")
			}
		}
	}
	if !found {
		out = append(out, wf+": no Build and push step")
	}
	return out
}

func TestProdIDVars_EveryAuthReadsTheVariable(t *testing.T) {
	t.Parallel()
	for _, wf := range prodIDWorkflows {
		if got := wifProblems(wf, readWorkflow(t, wf)); len(got) != 0 {
			t.Errorf("%v", got)
		}
	}
	mutated := strings.Replace(readWorkflow(t, "rollback-prod.yml"), "workload_identity_provider: "+wifVar, "workload_identity_provider: projects/000000000000/locations/global/workloadIdentityPools/p/providers/q", 1)
	if got := wifProblems("rollback-prod.yml", mutated); len(got) != 1 {
		t.Errorf("a literal provider was not reported: %v", got)
	}
}

func TestProdIDVars_WorkflowEnvReadsTheVariable(t *testing.T) {
	t.Parallel()
	want := map[string]string{"WIF_PROVIDER": wifVar}
	for _, wf := range []string{"deploy-prod.yml", "dev-env.yml"} {
		yml := readWorkflow(t, wf)
		if got := envProblems(wf, yml, want); len(got) != 0 {
			t.Errorf("%v", got)
		}
		mutated := strings.Replace(yml, "WIF_PROVIDER: "+wifVar, "WIF_PROVIDER: projects/000000000000/x", 1)
		if got := envProblems(wf, mutated, want); len(got) != 1 {
			t.Errorf("%s: a literal WIF_PROVIDER was not reported: %v", wf, got)
		}
	}
}

func TestProdIDVars_TheCheckRunsBeforeTheFirstGCPCall(t *testing.T) {
	t.Parallel()
	for i, r := range checkRoots {
		yml := readWorkflow(t, r.wf)
		if got := checkProblems(yml, i); len(got) != 0 {
			t.Errorf("%v", got)
		}
	}
	rb := slices.IndexFunc(checkRoots, func(r checkRoot) bool {
		return r.job == "rollback"
	})
	yml := readWorkflow(t, "rollback-prod.yml")
	block := checkBlock.FindString(yml)
	if block == "" {
		t.Fatal("no check block in rollback-prod.yml")
	}
	moved := strings.Replace(strings.Replace(yml, block, "", 1), "      - name: Move traffic", block+"      - name: Move traffic", 1)
	if got := checkProblems(moved, rb); !slices.ContainsFunc(got, func(p string) bool { return strings.Contains(p, "after auth") }) {
		t.Errorf("a check moved after auth was not reported: %v", got)
	}
	td := slices.IndexFunc(checkRoots, func(r checkRoot) bool {
		return r.inline
	})
	tdYml := readWorkflow(t, "dev-env-teardown.yml")
	inline := regexp.MustCompile(`(?m)^        run: '\[ -n .*$`)
	if !inline.MatchString(tdYml) {
		t.Fatal("no inline check run line in dev-env-teardown.yml")
	}
	swapped := inline.ReplaceAllString(tdYml, "        run: bash scripts/ci/require-vars.sh GCP_WIF_PROVIDER")
	if got := checkProblems(swapped, td); !slices.ContainsFunc(got, func(p string) bool { return strings.Contains(p, "scripts/ci/") }) {
		t.Errorf("a script call in the teardown check was not reported: %v", got)
	}
}

func TestProdIDVars_EveryAuthJobReachesACheckedRoot(t *testing.T) {
	t.Parallel()
	for _, wf := range prodIDWorkflows {
		if got := authReachProblems(wf, readWorkflow(t, wf)); len(got) != 0 {
			t.Errorf("%v", got)
		}
	}
	rogue := "\n  rogue:\n    needs: []\n    runs-on: ubuntu-latest\n    steps:\n      - uses: google-github-actions/auth@v3\n        with:\n          workload_identity_provider: ${{ env.WIF_PROVIDER }}\n"
	got := authReachProblems("dev-env.yml", readWorkflow(t, "dev-env.yml")+rogue)
	if len(got) != 1 || !strings.Contains(got[0], "rogue") {
		t.Errorf("an unchecked auth job was not reported: %v", got)
	}
}

func TestProdIDVars_SpaBuildsReadThePortalVariable(t *testing.T) {
	t.Parallel()
	for _, wf := range []string{"deploy-prod.yml", "dev-env.yml"} {
		yml := readWorkflow(t, wf)
		if got := spaBuildProblems(wf, yml); len(got) != 0 {
			t.Errorf("%v", got)
		}
		mutated := strings.Replace(yml, `"VITE_HUBSPOT_PORTAL_ID=$HUBSPOT_PORTAL_ID"`, "VITE_HUBSPOT_PORTAL_ID=1234567", 1)
		if got := spaBuildProblems(wf, mutated); len(got) != 2 {
			t.Errorf("%s: a literal portal id was not reported: %v", wf, got)
		}
	}
}

const nightlyRun = "go run ./internal/tools/nightlycontacts"

// nightlyLiveProblems reports each nightlycontacts command line whose own line or "\\"-continued predecessor lacks the live portal prefix.
func nightlyLiveProblems(yml string) (problems []string, lines int) {
	const prefix = "HUBSPOT_LIVE_PORTAL_ID=$HUBSPOT_PORTAL_ID"
	all := strings.Split(yml, "\n")
	for i, l := range all {
		if !strings.Contains(l, nightlyRun) {
			continue
		}
		lines++
		if strings.Contains(l, prefix) || (i > 0 && strings.HasSuffix(strings.TrimSpace(all[i-1]), "\\") && strings.Contains(all[i-1], prefix)) {
			continue
		}
		problems = append(problems, fmt.Sprintf("dev-env.yml:%d: nightlycontacts lacks %s", i+1, prefix))
	}
	return problems, lines
}

func TestProdIDVars_NightlyContactsGetTheLivePortal(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "dev-env.yml")
	got, n := nightlyLiveProblems(yml)
	if n != 3 {
		t.Fatalf("%d nightlycontacts lines, want 3", n)
	}
	if len(got) != 0 {
		t.Errorf("%v", got)
	}
	const prefix = "HUBSPOT_LIVE_PORTAL_ID=$HUBSPOT_PORTAL_ID "
	idx := strings.LastIndex(yml, prefix)
	mutated := yml[:idx] + yml[idx+len(prefix):]
	if got, _ := nightlyLiveProblems(mutated); len(got) != 1 {
		t.Errorf("a finish line without the prefix was not reported: %v", got)
	}
}

// guardRollUpProblems reports a ci roll-up that does not require the prod-ids job.
func guardRollUpProblems(yml string) []string {
	j, ok := ciJobOf(yml, "ci")
	if !ok {
		return []string{"ci.yml has no ci job"}
	}
	var p []string
	needs, _ := jobKey(j, "needs")
	if !slices.Contains(strings.FieldsFunc(needs, func(r rune) bool { return strings.ContainsRune("[], ", r) }), "prod-ids") {
		p = append(p, "ci needs lacks prod-ids")
	}
	if !strings.Contains(strings.Join(j.lines, "\n"), `needs.prod-ids.result }}" != "success"`) {
		p = append(p, "ci run block does not require prod-ids to succeed")
	}
	return p
}

func TestProdIDVars_GuardIsRequiredByTheRollUp(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "ci.yml")
	if got := guardRollUpProblems(yml); len(got) != 0 {
		t.Errorf("%v", got)
	}
	noNeeds := mutateOnce(t, yml, "docling-test, prod-ids, rls", "docling-test, rls")
	if got := guardRollUpProblems(noNeeds); len(got) != 1 {
		t.Errorf("a roll-up without prod-ids in needs was not reported: %v", got)
	}
	noGate := mutateOnce(t, yml, `needs.prod-ids.result }}" != "success"`, `needs.prod-ids.result }}" = "failure"`)
	if got := guardRollUpProblems(noGate); len(got) != 1 {
		t.Errorf("a roll-up that tolerates a skipped prod-ids was not reported: %v", got)
	}
}
