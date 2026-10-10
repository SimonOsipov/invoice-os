// deploy_slot_workflow_test.go pins the deploy-slot wiring of .github/workflows/dev-env.yml.
// Constants mirror the workflow wiring of dev-env.yml.
package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	slotJobID      = "deploy-slot"
	slotJobName    = "Deploy slot" // the script matches this name
	releaseJobID   = "deploy-slot-release"
	releaseJobName = "Release deploy slot"
	changesJobName = "Detect E2E-relevant changes" // the script's no-slot-job grace reads it

	// the script's chain-ended rule reads these names
	prepareJobName = "Prepare Railway environment (create-or-reuse + assert Watch Paths + discover URLs)"
	healthJobName  = "Gate on gateway /healthz (schema migrated)"
	fleetJobName   = "Fleet /healthz gate (all 10 backends green)"
)

// releaseNeeds is the eight-job set the release job waits on.
var releaseNeeds = []string{"deploy-slot", "prepare-env", "deploy-gateway", "health-gate", "deploy-context", "deploy-spas", "fleet-gate", "deploy-library"}

func devEnvJob(t *testing.T, id string) workflowJob {
	t.Helper()
	for _, j := range workflowJobsOf(readWorkflow(t, "dev-env.yml")) {
		if j.name == id {
			return j
		}
	}
	t.Fatalf("dev-env.yml has no job %q", id)
	return workflowJob{}
}

// jobKey returns a job-level key's inline value plus its indented continuation lines, joined by spaces.
func jobKey(j workflowJob, key string) (string, bool) {
	for i, l := range j.lines {
		if strings.HasPrefix(l, "    "+key+":") && !strings.HasPrefix(l, "     ") {
			parts := []string{strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), key+":"))}
			for _, n := range j.lines[i+1:] {
				if strings.TrimSpace(n) != "" && !strings.HasPrefix(n, "     ") {
					break
				}
				parts = append(parts, strings.TrimSpace(n))
			}
			return strings.TrimSpace(strings.Join(parts, " ")), true
		}
	}
	return "", false
}

// blockLines returns the trimmed, non-empty child lines of a top-level or job-level block key.
func blockLines(lines []string, indent int, key string) []string {
	var out []string
	pad := strings.Repeat(" ", indent)
	in := false
	for _, l := range stripHashComments(lines) {
		if strings.TrimSpace(l) == "" {
			continue
		}
		cur := len(l) - len(strings.TrimLeft(l, " "))
		if !in {
			in = cur == indent && strings.HasPrefix(l, pad+key+":")
			continue
		}
		if cur <= indent {
			break
		}
		out = append(out, strings.TrimSpace(l))
	}
	return out
}

func sorted(s []string) []string {
	c := slices.Clone(s)
	sort.Strings(c)
	return c
}

func TestDevEnvDeploySlotJobRunsTheScript(t *testing.T) {
	j := devEnvJob(t, slotJobID)
	if v, _ := jobKey(j, "name"); v != slotJobName {
		t.Errorf("deploy-slot name = %q, want %q", v, slotJobName)
	}
	if got := jobList(j, "needs"); !slices.Equal(got, []string{"changes"}) {
		t.Errorf("deploy-slot needs = %v, want [changes]", got)
	}
	var env map[string]string
	runs := 0
	for _, s := range j.steps() {
		if strings.Contains(s.keys["run"], "scripts/ci/deploy-slot.sh") {
			runs++
			env = s.env
		}
	}
	if runs != 1 {
		t.Fatalf("deploy-slot has %d steps running scripts/ci/deploy-slot.sh, want 1", runs)
	}
	for k, want := range map[string]string{"RUN_ID": "${{ github.run_id }}", "EVENT_NAME": "${{ github.event_name }}"} {
		if env[k] != want {
			t.Errorf("env %s = %q, want %q", k, env[k], want)
		}
	}
	for k, want := range map[string]string{"REPO": "${{ github.repository }}", "GH_TOKEN": "${{ github.token }}"} {
		if env[k] != want {
			t.Errorf("env %s = %q, want %q", k, env[k], want)
		}
	}
	checkout, script := -1, -1
	for i, s := range j.steps() {
		if strings.HasPrefix(s.keys["uses"], "actions/checkout@") && checkout < 0 {
			checkout = i
		}
		if strings.Contains(s.keys["run"], "scripts/ci/deploy-slot.sh") {
			script = i
		}
	}
	if checkout < 0 || checkout > script {
		t.Errorf("deploy-slot must check out the repo before running the script (checkout step %d, script step %d)", checkout, script)
	}
	if v, _ := jobKey(devEnvJob(t, "changes"), "name"); v != changesJobName {
		t.Errorf("changes name = %q, want %q", v, changesJobName)
	}
	for id, want := range map[string]string{"prepare-env": prepareJobName, "health-gate": healthJobName, "fleet-gate": fleetJobName} {
		if v, _ := jobKey(devEnvJob(t, id), "name"); v != want {
			t.Errorf("%s name = %q, want %q", id, v, want)
		}
	}
	for _, id := range []string{"deploy-gateway", "deploy-context", "deploy-spas"} {
		if v, _ := jobKey(devEnvJob(t, id), "name"); !strings.HasPrefix(v, "Deploy ") || !strings.Contains(v, " → ") {
			t.Errorf("%s name = %q, want \"Deploy <x> → <env>\"", id, v)
		}
	}
}

func TestDevEnvDeploySlotSkipsDraftsAndIrrelevantRuns(t *testing.T) {
	cond, ok := jobKey(devEnvJob(t, slotJobID), "if")
	if !ok {
		t.Fatal("deploy-slot has no if:")
	}
	for _, want := range []string{"github.event.pull_request.draft == false", "needs.changes.outputs.e2e == 'true'"} {
		if !strings.Contains(cond, want) {
			t.Errorf("deploy-slot if: %q lacks %q", cond, want)
		}
	}
	if strings.Contains(cond, "always()") {
		t.Errorf("deploy-slot if: %q has always(); a cancelled or skipped run would hold a slot", cond)
	}
}

func TestPrepareEnvRequiresTheDeploySlot(t *testing.T) {
	j := devEnvJob(t, "prepare-env")
	needs := jobList(j, "needs")
	for _, want := range []string{"deploy-slot", "changes"} {
		if !slices.Contains(needs, want) {
			t.Errorf("prepare-env needs = %v, lacks %q", needs, want)
		}
	}
	if cond, _ := jobKey(j, "if"); !strings.Contains(cond, "needs.deploy-slot.result == 'success'") {
		t.Errorf("prepare-env if: %q lacks needs.deploy-slot.result == 'success'", cond)
	}
}

func TestDeploySlotReleaseNeedsTheWholeDeployChain(t *testing.T) {
	j := devEnvJob(t, releaseJobID)
	if v, _ := jobKey(j, "name"); v != releaseJobName {
		t.Errorf("name = %q, want %q", v, releaseJobName)
	}
	if got := sorted(jobList(j, "needs")); !slices.Equal(got, sorted(releaseNeeds)) {
		t.Errorf("needs = %v, want %v", got, sorted(releaseNeeds))
	}
	cond, _ := jobKey(j, "if")
	for _, want := range []string{"always()", "needs.deploy-slot.result == 'success'"} {
		if !strings.Contains(cond, want) {
			t.Errorf("if: %q lacks %q", cond, want)
		}
	}
}

func TestNoJobWaitsOnTheSlotRelease(t *testing.T) {
	seen := map[string]bool{}
	for _, j := range workflowJobsOf(readWorkflow(t, "dev-env.yml")) {
		seen[j.name] = true
		if slices.Contains(jobList(j, "needs"), releaseJobID) {
			t.Errorf("job %s needs %s; the release must be a leaf", j.name, releaseJobID)
		}
	}
	for _, id := range []string{"prepare-env", "fleet-gate", "e2e-gate"} {
		if !seen[id] {
			t.Errorf("control: parse did not find job %s", id)
		}
	}
}

func TestDeploySlotJobsHoldNoRailwayCredential(t *testing.T) {
	for _, id := range []string{slotJobID, releaseJobID} {
		j := devEnvJob(t, id)
		if len(j.lines) == 0 {
			t.Fatalf("control: job %s has no lines", id)
		}
		if jobCallsRailway(j.lines) {
			t.Errorf("job %s calls railway-env.sh", id)
		}
		for _, l := range j.lines {
			if strings.Contains(l, "secrets.") || strings.Contains(l, "railway-up-ci.sh") {
				t.Errorf("job %s line %q references a secret or railway-up-ci.sh", id, strings.TrimSpace(l))
			}
		}
	}
}

func TestDeploySlotTimeoutBoundsTheWait(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "ci", "deploy-slot.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	m := regexp.MustCompile(`(?m)^DEADLINE_SECONDS=(\d+)`).FindStringSubmatch(script)
	if m == nil {
		t.Fatal("control: no DEADLINE_SECONDS=<n> line in scripts/ci/deploy-slot.sh")
	}
	deadline, _ := strconv.Atoi(m[1])
	if deadline <= 0 {
		t.Fatalf("control: DEADLINE_SECONDS = %d, want > 0", deadline)
	}
	v, ok := jobKey(devEnvJob(t, slotJobID), "timeout-minutes")
	if !ok {
		t.Fatal("deploy-slot has no timeout-minutes")
	}
	mins, err := strconv.Atoi(v)
	if err != nil {
		t.Fatalf("timeout-minutes %q is not an integer", v)
	}
	if mins > 60 {
		t.Errorf("timeout-minutes = %d, want <= 60", mins)
	}
	if mins*60 < deadline+600 {
		t.Errorf("timeout-minutes = %d (%d s), want >= DEADLINE_SECONDS %d + 600", mins, mins*60, deadline)
	}
}

func TestDeploySlotPermissions(t *testing.T) {
	slot := blockLines(devEnvJob(t, slotJobID).lines, 4, "permissions")
	if !slices.Equal(sorted(slot), []string{"actions: read", "contents: read"}) {
		t.Errorf("deploy-slot permissions = %v, want actions: read + contents: read", slot)
	}
	if v, ok := jobKey(devEnvJob(t, releaseJobID), "permissions"); !ok || v != "{}" {
		t.Errorf("deploy-slot-release permissions = %q (present %v), want {}", v, ok)
	}
	top := blockLines(strings.Split(readWorkflow(t, "dev-env.yml"), "\n"), 0, "permissions")
	if !slices.Equal(sorted(top), []string{"checks: read", "contents: read"}) {
		t.Errorf("workflow permissions = %v, want checks: read + contents: read", top)
	}
}

func TestDeploySlotReleaseWaitsForEveryJobThatDeploys(t *testing.T) {
	needs := jobList(devEnvJob(t, releaseJobID), "needs")
	deployers := 0
	for _, j := range workflowJobsOf(readWorkflow(t, "dev-env.yml")) {
		if j.name == releaseJobID {
			continue
		}
		for _, l := range stripHashComments(j.lines) {
			if strings.Contains(l, "railway-up-ci.sh") {
				deployers++
				if !slices.Contains(needs, j.name) {
					t.Errorf("job %s runs railway-up-ci.sh but deploy-slot-release does not need it; the slot would free mid-deploy", j.name)
				}
				break
			}
		}
	}
	if deployers == 0 {
		t.Fatal("control: no job runs railway-up-ci.sh")
	}
}

func TestDeploySlotAndPrepareEnvShareTheRelevanceGate(t *testing.T) {
	slot, _ := jobKey(devEnvJob(t, slotJobID), "if")
	prep, _ := jobKey(devEnvJob(t, "prepare-env"), "if")
	if slot == "" || prep == "" {
		t.Fatal("control: deploy-slot or prepare-env has no if:")
	}
	for _, want := range []string{
		"github.event_name == 'workflow_dispatch'", "github.event_name == 'push'",
		"github.event.pull_request.draft == false", "needs.changes.outputs.e2e == 'true'",
	} {
		if !strings.Contains(slot, want) || !strings.Contains(prep, want) {
			t.Errorf("deploy-slot and prepare-env must both gate on %q; a skipped slot would silently skip Prepare\n slot: %s\n prep: %s", want, slot, prep)
		}
	}
}
