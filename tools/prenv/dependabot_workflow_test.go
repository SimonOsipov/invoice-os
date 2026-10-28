// dependabot_workflow_test.go pins that no job holding an OIDC token runs on a Dependabot PR (PATCH-02).
package main

import (
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

const authorCond = "github.event.pull_request.user.login != 'dependabot[bot]'"

var needsSuccess = regexp.MustCompile(`^needs\.([A-Za-z0-9_-]+)\.result == 'success'$`)

// foldedIf returns a job's if: on one line, without the block marker or surrounding quotes.
func foldedIf(j workflowJob) (string, bool) {
	v, ok := jobKey(j, "if")
	if !ok {
		return "", false
	}
	v = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(v, ">-"), ">"))
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		v = v[1 : len(v)-1]
	}
	return v, true
}

// topLevelConjuncts splits expr on && outside parentheses and string literals.
func topLevelConjuncts(expr string) []string {
	var out []string
	depth, start, quoted := 0, 0, false
	for i := 0; i < len(expr); i++ {
		switch c := expr[i]; {
		case c == '\'':
			quoted = !quoted
		case quoted:
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == '&' && depth == 0 && i+1 < len(expr) && expr[i+1] == '&':
			out = append(out, strings.TrimSpace(expr[start:i]))
			start = i + 2
			i++
		}
	}
	return append(out, strings.TrimSpace(expr[start:]))
}

// hasAuthorConjunct reports whether expr has no || and a top-level && conjunct equal to the author condition.
func hasAuthorConjunct(expr string) bool {
	return !strings.Contains(expr, "||") && slices.Contains(topLevelConjuncts(expr), authorCond)
}

// guardedJobs is the fixpoint of the jobs that skip on a Dependabot PR: directly, or through a
// needs.<guarded>.result == 'success' conjunct (a skipped upstream is not 'success').
func guardedJobs(jobs []workflowJob) map[string]bool {
	guarded := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for _, j := range jobs {
			expr, ok := foldedIf(j)
			if guarded[j.name] || !ok || strings.Contains(expr, "||") {
				continue
			}
			for _, c := range topLevelConjuncts(expr) {
				m := needsSuccess.FindStringSubmatch(c)
				if c == authorCond || (m != nil && guarded[m[1]]) {
					guarded[j.name], changed = true, true
					break
				}
			}
		}
	}
	return guarded
}

func oidcJobs(jobs []workflowJob) []string {
	var out []string
	for _, j := range jobs {
		for _, l := range blockLines(j.lines, 4, "permissions") {
			if k, v, _ := strings.Cut(l, ":"); strings.TrimSpace(k) == "id-token" && strings.TrimSpace(v) == "write" {
				out = append(out, j.name)
			}
		}
	}
	return out
}

// unguardedOIDCJobs lists the jobs that request id-token: write and can run on a Dependabot PR.
func unguardedOIDCJobs(yml string) []string {
	jobs := workflowJobsOf(yml)
	guarded := guardedJobs(jobs)
	var out []string
	for _, name := range oidcJobs(jobs) {
		if !guarded[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// teardownSkipsDependabot reports whether the teardown job's if: is exactly the author condition.
func teardownSkipsDependabot(yml string) bool {
	for _, j := range workflowJobsOf(yml) {
		if j.name == "teardown" {
			expr, ok := foldedIf(j)
			return ok && expr == authorCond
		}
	}
	return false
}

// ifsReadingActor returns every if: expression (job or step, folded) that reads github.actor.
func ifsReadingActor(yml string) []string {
	var out []string
	lines := stripHashComments(strings.Split(yml, "\n"))
	for i, l := range lines {
		body := strings.TrimPrefix(strings.TrimSpace(l), "- ")
		if !strings.HasPrefix(body, "if:") {
			continue
		}
		indent := len(l) - len(strings.TrimLeft(l, " "))
		expr := body
		for _, n := range lines[i+1:] {
			if strings.TrimSpace(n) != "" && len(n)-len(strings.TrimLeft(n, " ")) <= indent {
				break
			}
			expr += " " + strings.TrimSpace(n)
		}
		if strings.Contains(expr, "github.actor") {
			out = append(out, expr)
		}
	}
	return out
}

// mutateJob applies mutateOnce to the raw text of one job, so a needle shared by sibling jobs stays unique.
func mutateJob(t *testing.T, yml, id, old, repl string) string {
	t.Helper()
	lines := strings.Split(yml, "\n")
	start, inJobs := -1, false
	for i, l := range lines {
		inJobs = inJobs || l == "jobs:"
		if inJobs && l == "  "+id+":" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("workflow has no job %q", id)
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		l, tr := lines[i], strings.TrimSpace(lines[i])
		topKey := tr != "" && !strings.HasPrefix(l, " ")
		header := strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "   ") && strings.HasSuffix(tr, ":") && !strings.HasPrefix(tr, "#")
		if topKey || header {
			end = i
			break
		}
	}
	block := strings.Join(lines[start:end], "\n")
	if end < len(lines) {
		block += "\n"
	}
	if n := strings.Count(block, old); n != 1 {
		t.Fatalf("job %q: mutation needle %q occurs %d times, want 1", id, old, n)
	}
	block = mutateOnce(t, block, old, repl)
	head := strings.Join(lines[:start], "\n")
	if start > 0 {
		head += "\n"
	}
	return head + block + strings.Join(lines[end:], "\n")
}

// mutantUnguarded returns the unguarded OIDC jobs after one in-job mutation of the real dev-env.yml.
// The control makes the mutant a real test of the guard: a file that is already unguarded fails here.
func mutantUnguarded(t *testing.T, id, old, repl string) []string {
	t.Helper()
	yml := readWorkflow(t, "dev-env.yml")
	if got := unguardedOIDCJobs(yml); len(got) != 0 {
		t.Fatalf("control: the real dev-env.yml has unguarded OIDC jobs %v", got)
	}
	return unguardedOIDCJobs(mutateJob(t, yml, id, old, repl))
}

func requireListed(t *testing.T, got []string, want string) {
	t.Helper()
	if !slices.Contains(got, want) {
		t.Errorf("unguardedOIDCJobs = %v, want it to list %q", got, want)
	}
}

func TestDevEnvDependabot_EveryOIDCJobIsGuarded(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "dev-env.yml")
	oidc := oidcJobs(workflowJobsOf(yml))
	for _, want := range []string{"invariants", "prepare-env", "build", "deploy", "fleet-gate", "e2e", "topology"} {
		if !slices.Contains(oidc, want) {
			t.Errorf("control: id-token jobs %v lack %q", oidc, want)
		}
	}
	if got := unguardedOIDCJobs(yml); len(got) != 0 {
		t.Errorf("OIDC jobs that can run on a Dependabot PR: %v", got)
	}
}

func TestDevEnvDependabot_RootJobsCarryTheAuthorCondition(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"invariants", "ci-watch", "prepare-env", "build"} {
		expr, ok := foldedIf(devEnvJob(t, id))
		if !ok || !hasAuthorConjunct(expr) {
			t.Errorf("%s if = %q, want a top-level && conjunct %q and no ||", id, expr, authorCond)
		}
	}
}

func TestDevEnvDependabot_NoIfReadsTheActor(t *testing.T) {
	t.Parallel()
	if got := ifsReadingActor(readWorkflow(t, "dev-env.yml")); len(got) != 0 {
		t.Errorf("if: expressions that read github.actor: %q", got)
	}
}

func TestDevEnvDependabot_InvariantsUnguardedFails(t *testing.T) {
	t.Parallel()
	got := mutantUnguarded(t, "invariants", "    if: "+authorCond+"\n", "")
	if !slices.Equal(got, []string{"invariants"}) {
		t.Errorf("unguardedOIDCJobs = %v, want [invariants]", got)
	}
}

func TestDevEnvDependabot_BuildUnguardedFails(t *testing.T) {
	t.Parallel()
	got := mutantUnguarded(t, "build", " && "+authorCond, "")
	if !slices.Equal(got, []string{"build"}) {
		t.Errorf("unguardedOIDCJobs = %v, want [build] (deploy stays guarded through prepare-env)", got)
	}
}

func TestDevEnvDependabot_ActorInsteadOfAuthorFails(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "dev-env.yml")
	if got := ifsReadingActor(yml); len(got) != 0 {
		t.Fatalf("control: the real dev-env.yml has an if: that reads github.actor: %q", got)
	}
	got := mutantUnguarded(t, "prepare-env", "github.event.pull_request.user.login", "github.actor")
	requireListed(t, got, "prepare-env")
	mutated := mutateJob(t, yml, "prepare-env", "github.event.pull_request.user.login", "github.actor")
	if len(ifsReadingActor(mutated)) == 0 {
		t.Error("the no-actor scan misses github.actor in a mutated prepare-env if:")
	}
}

func TestDevEnvDependabot_DeployIfLosingNeedsFails(t *testing.T) {
	t.Parallel()
	got := mutantUnguarded(t, "deploy",
		`if: "!cancelled() && needs.prepare-env.result == 'success' && needs.build.result == 'success'"`,
		`if: "!cancelled()"`)
	requireListed(t, got, "deploy")
}

func TestDevEnvDependabot_DeployOrInsteadOfAndFails(t *testing.T) {
	t.Parallel()
	got := mutantUnguarded(t, "deploy", "'success' && needs.build.result", "'success' || needs.build.result")
	requireListed(t, got, "deploy")
}

func TestDevEnvDependabot_NotEqualSuccessIsNotAGuard(t *testing.T) {
	t.Parallel()
	got := mutantUnguarded(t, "fleet-gate", "needs.deploy.result == 'success'", "needs.deploy.result != 'success'")
	requireListed(t, got, "fleet-gate")
}

func TestDevEnvTeardown_SkipsDependabotPRs(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "dev-env-teardown.yml")
	if !teardownSkipsDependabot(yml) {
		t.Fatalf("teardown if is not exactly %q", authorCond)
	}
	if teardownSkipsDependabot(mutateOnce(t, yml, "    if: "+authorCond+"\n", "")) {
		t.Error("teardownSkipsDependabot stays true after the if: is deleted")
	}
}

func TestDevEnvDependabot_NewOIDCJobUnguardedFails(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "dev-env.yml")
	if got := unguardedOIDCJobs(yml); len(got) != 0 {
		t.Fatalf("control: the real dev-env.yml has unguarded OIDC jobs %v", got)
	}
	extra := "\n  extra-oidc:\n    runs-on: ubuntu-latest\n    permissions:\n      id-token: write\n    steps:\n      - run: echo hi\n"
	requireListed(t, unguardedOIDCJobs(yml+extra), "extra-oidc")
}

func TestDevEnvDependabot_WorkflowPermissionsHoldNoIDToken(t *testing.T) {
	t.Parallel()
	block := blockLines(strings.Split(readWorkflow(t, "dev-env.yml"), "\n"), 0, "permissions")
	if len(block) == 0 {
		t.Fatal("control: dev-env.yml has no top-level permissions block")
	}
	for _, l := range block {
		if strings.HasPrefix(l, "id-token") {
			t.Errorf("workflow-level permissions hold %q: every job would inherit an OIDC token", l)
		}
	}
}

func TestDevEnvDependabot_GateReadsThePRAuthor(t *testing.T) {
	t.Parallel()
	const want = "${{ github.event.pull_request.user.login }}"
	for _, s := range devEnvJob(t, "e2e-gate").steps() {
		if !strings.Contains(s.keys["run"], "node e2e/mergeGate.ts") {
			continue
		}
		if s.env["PR_AUTHOR"] != want {
			t.Errorf("gate env PR_AUTHOR = %q, want %q", s.env["PR_AUTHOR"], want)
		}
		return
	}
	t.Fatal("e2e-gate has no node e2e/mergeGate.ts step")
}

func TestDevEnvDependabot_InvariantsHasNoOtherCondition(t *testing.T) {
	t.Parallel()
	expr, ok := foldedIf(devEnvJob(t, "invariants"))
	if !ok || expr != authorCond {
		t.Errorf("invariants if = %q, want exactly %q (no draft or path term)", expr, authorCond)
	}
}
