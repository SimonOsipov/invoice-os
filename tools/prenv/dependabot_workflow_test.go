// dependabot_workflow_test.go pins that no job holding an OIDC token runs on a Dependabot PR.
package main

import (
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

const (
	authorCond   = "needs.commit-authors.outputs.dependabot_only == 'false'"
	teardownCond = "github.event.pull_request.user.login != 'dependabot[bot]'"
)

var rootJobs = []string{"invariants", "ci-watch", "prepare-env", "build"}

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

// teardownSkipsDependabot reports whether the teardown job's if: is exactly the PR-author condition.
func teardownSkipsDependabot(yml string) bool {
	for _, j := range workflowJobsOf(yml) {
		if j.name == "teardown" {
			expr, ok := foldedIf(j)
			return ok && expr == teardownCond
		}
	}
	return false
}

// needsOf lists a job's needs, from a scalar or a flow list.
func needsOf(j workflowJob) []string {
	v, ok := jobKey(j, "needs")
	if !ok {
		return nil
	}
	var out []string
	for _, f := range strings.Split(strings.Trim(v, "[]"), ",") {
		out = append(out, strings.TrimSpace(f))
	}
	return out
}

// rootJobsLackingAuthorCond lists the root jobs without the exact author conjunct or without commit-authors in needs.
func rootJobsLackingAuthorCond(yml string) []string {
	byName := map[string]workflowJob{}
	for _, j := range workflowJobsOf(yml) {
		byName[j.name] = j
	}
	var bad []string
	for _, id := range rootJobs {
		j, found := byName[id]
		expr, hasIf := foldedIf(j)
		if !found || !hasIf || !hasAuthorConjunct(expr) || !slices.Contains(needsOf(j), "commit-authors") {
			bad = append(bad, id)
		}
	}
	return bad
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
	if slices.Contains(oidc, "commit-authors") {
		t.Error("commit-authors requests id-token: it runs on every PR, Dependabot's included")
	}
	if got := unguardedOIDCJobs(yml); len(got) != 0 {
		t.Errorf("OIDC jobs that can run on a Dependabot PR: %v", got)
	}
}

func TestDevEnvDependabot_RootJobsCarryTheAuthorCondition(t *testing.T) {
	t.Parallel()
	if got := rootJobsLackingAuthorCond(readWorkflow(t, "dev-env.yml")); len(got) != 0 {
		t.Errorf("jobs without a top-level && conjunct %q, no ||, and commit-authors in needs: %v", authorCond, got)
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
		t.Fatalf("teardown if is not exactly %q", teardownCond)
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

func TestDevEnvDependabot_GateReadsTheCommitVerdict(t *testing.T) {
	t.Parallel()
	gate := devEnvJob(t, "e2e-gate")
	if !slices.Contains(needsOf(gate), "commit-authors") {
		t.Errorf("e2e-gate needs = %v, want commit-authors", needsOf(gate))
	}
	want := map[string]string{
		"COMMITS_ONLY_DEPENDABOT": "${{ needs.commit-authors.outputs.dependabot_only }}",
		"PR_AUTHOR":               "${{ github.event.pull_request.user.login }}",
	}
	for _, s := range gate.steps() {
		if !strings.Contains(s.keys["run"], "node e2e/mergeGate.ts") {
			continue
		}
		for k, v := range want {
			if s.env[k] != v {
				t.Errorf("gate env %s = %q, want %q", k, s.env[k], v)
			}
		}
		return
	}
	t.Fatal("e2e-gate has no node e2e/mergeGate.ts step")
}

func TestDevEnvDependabot_CommitAuthorsHoldsNoIDToken(t *testing.T) {
	t.Parallel()
	j := devEnvJob(t, "commit-authors")
	if grantsIDToken(j.lines, 4) {
		t.Error("commit-authors requests id-token: write")
	}
	perms := blockLines(j.lines, 4, "permissions")
	sort.Strings(perms)
	if want := []string{"contents: read", "pull-requests: read"}; !slices.Equal(perms, want) {
		t.Errorf("commit-authors permissions = %v, want exactly %v", perms, want)
	}
	for _, k := range []string{"needs", "if"} {
		if v, ok := jobKey(j, k); ok {
			t.Errorf("commit-authors has %s: %q, want none", k, v)
		}
	}
	if got := blockLines(j.lines, 4, "outputs"); !slices.Equal(got, []string{"dependabot_only: ${{ steps.verdict.outputs.dependabot_only }}"}) {
		t.Errorf("commit-authors outputs = %v", got)
	}
	for _, s := range j.steps() {
		if s.keys["id"] != "verdict" {
			continue
		}
		if want := `bash scripts/ci/dependabot-only.sh "$PR"`; s.keys["run"] != want {
			t.Errorf("verdict run = %q, want %q", s.keys["run"], want)
		}
		for k, want := range map[string]string{
			"GH_TOKEN":     "${{ github.token }}",
			"REPO":         "${{ github.repository }}",
			"PR_AUTHOR":    "${{ github.event.pull_request.user.login }}",
			"EVENT_SENDER": "${{ github.event.sender.login }}",
			"EVENT_ACTION": "${{ github.event.action }}",
			"HEAD_SHA":     "${{ github.event.pull_request.head.sha }}",
		} {
			if s.env[k] != want {
				t.Errorf("verdict env %s = %q, want %q", k, s.env[k], want)
			}
		}
		return
	}
	t.Fatal("commit-authors has no step with id: verdict")
}

func TestDevEnvDependabot_RootJobsKeepTheirOtherConditions(t *testing.T) {
	t.Parallel()
	ready := "github.event.pull_request.draft == false && needs.changes.outputs.e2e == 'true' && " + authorCond
	for _, c := range []struct {
		id    string
		needs []string
		cond  string
	}{
		{"invariants", []string{"commit-authors"}, authorCond},
		{"ci-watch", []string{"changes", "commit-authors"}, ready},
		{"prepare-env", []string{"changes", "commit-authors"}, ready},
		{"build", []string{"changes", "commit-authors"}, ready},
	} {
		j := devEnvJob(t, c.id)
		if expr, ok := foldedIf(j); !ok || expr != c.cond {
			t.Errorf("%s if = %q, want exactly %q", c.id, expr, c.cond)
		}
		if got := needsOf(j); !slices.Equal(got, c.needs) {
			t.Errorf("%s needs = %v, want %v", c.id, got, c.needs)
		}
	}
}
