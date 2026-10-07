// deploy_slot_workflow_test.go pins the deploy-slot wiring of .github/workflows/dev-env.yml.
// Source of every constant: INFRA-08 story, Design "Workflow wiring"; subtask INFRA-08-02 ACs.
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
	slotJobID      = "deploy-slot"                 // story Design "Workflow wiring"
	slotJobName    = "Deploy slot"                 // story Design; the script matches this name
	releaseJobID   = "deploy-slot-release"         // story Design
	releaseJobName = "Release deploy slot"         // story Design
	changesJobName = "Detect E2E-relevant changes" // 02-1; the script's no-slot-job grace reads it (D-05)
)

// releaseNeeds is the seven-job set of 02-3.
var releaseNeeds = []string{"deploy-slot", "prepare-env", "deploy-gateway", "health-gate", "deploy-context", "deploy-spas", "fleet-gate"}

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
	for _, k := range []string{"REPO", "GH_TOKEN"} {
		if env[k] == "" {
			t.Errorf("env %s is not set on the script step", k)
		}
	}
	if v, _ := jobKey(devEnvJob(t, "changes"), "name"); v != changesJobName {
		t.Errorf("changes name = %q, want %q", v, changesJobName)
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
