// dependabot_workflow_test.go pins that no job holding an OIDC token runs on a Dependabot PR.
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

// grantsIDToken reports whether the permissions key at indent grants id-token: write, as a block, a flow map or write-all.
func grantsIDToken(lines []string, indent int) bool {
	pad := strings.Repeat(" ", indent)
	for _, l := range stripHashComments(lines) {
		if rest, ok := strings.CutPrefix(l, pad+"permissions:"); ok {
			rest = strings.TrimSpace(rest)
			if rest == "write-all" {
				return true
			}
			if inner, ok := strings.CutPrefix(rest, "{"); ok {
				for _, f := range strings.Split(strings.TrimSuffix(inner, "}"), ",") {
					if k, v, _ := strings.Cut(f, ":"); strings.TrimSpace(k) == "id-token" && strings.TrimSpace(v) == "write" {
						return true
					}
				}
			}
		}
	}
	for _, l := range blockLines(lines, indent, "permissions") {
		if k, v, _ := strings.Cut(l, ":"); strings.TrimSpace(k) == "id-token" && strings.TrimSpace(v) == "write" {
			return true
		}
	}
	return false
}

func oidcJobs(jobs []workflowJob) []string {
	var out []string
	for _, j := range jobs {
		if grantsIDToken(j.lines, 4) {
			out = append(out, j.name)
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

func TestDevEnvTeardown_SkipsDependabotPRs(t *testing.T) {
	t.Parallel()
	yml := readWorkflow(t, "dev-env-teardown.yml")
	if !teardownSkipsDependabot(yml) {
		t.Fatalf("teardown if is not exactly %q", authorCond)
	}
}

func TestDevEnvDependabot_WorkflowPermissionsHoldNoIDToken(t *testing.T) {
	t.Parallel()
	lines := strings.Split(readWorkflow(t, "dev-env.yml"), "\n")
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "permissions:") }) {
		t.Fatal("control: dev-env.yml has no top-level permissions key")
	}
	if grantsIDToken(lines, 0) {
		t.Error("workflow-level permissions grant id-token: every job would inherit an OIDC token")
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
