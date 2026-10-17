// topology_workflow_test.go pins the topology job's gating in .github/workflows/dev-env.yml.
package main

import (
	"slices"
	"strings"
	"testing"
)

func TestDevEnvTopologyStartsOnlyAfterAGreenE2E(t *testing.T) {
	j := devEnvJob(t, "topology")
	cond, ok := jobKey(j, "if")
	if !ok {
		t.Fatal("topology has no if")
	}
	for _, want := range []string{"!cancelled()", "github.event_name == 'pull_request'", "needs.e2e.result == 'success'"} {
		if !strings.Contains(cond, want) {
			t.Errorf("topology if %q lacks %q", cond, want)
		}
	}
	if n := strings.Count(cond, "needs.e2e.result"); n != 1 {
		t.Errorf("topology if %q reads needs.e2e.result %d times, want once", cond, n)
	}
	if strings.Contains(cond, "||") {
		t.Errorf("topology if %q must not widen the e2e condition with ||", cond)
	}
	needs, _ := jobKey(j, "needs")
	var ids []string
	for _, id := range strings.Split(strings.Trim(needs, "[]"), ",") {
		ids = append(ids, strings.TrimSpace(id))
	}
	if !slices.Contains(ids, "e2e") {
		t.Errorf("topology needs %q lacks the job e2e", needs)
	}
}

func TestDevEnvTopologyKeepsSiblingShardsRunning(t *testing.T) {
	block := strings.Join(blockLines(devEnvJob(t, "topology").lines, 4, "strategy"), "\n")
	for _, want := range []string{"fail-fast: false", "shard: ${{ fromJSON(needs.e2e.outputs.shards) }}"} {
		if !strings.Contains(block, want) {
			t.Errorf("topology strategy lacks %q:\n%s", want, block)
		}
	}
	if strings.Contains(block, "max-parallel") {
		t.Errorf("topology strategy must not set max-parallel:\n%s", block)
	}
}

func TestDevEnvTopologyReportStepsRunAfterAStoppedShard(t *testing.T) {
	steps := devEnvJob(t, "topology").steps()
	find := func(name string) workflowStep {
		var got []workflowStep
		for _, s := range steps {
			if s.keys["name"] == name {
				got = append(got, s)
			}
		}
		if len(got) != 1 {
			t.Fatalf("want exactly one step %q, got %d", name, len(got))
		}
		return got[0]
	}
	for _, name := range []string{"Park the topology report", "Fail on a retry-pass in a spec this PR changed", "Upload topology Playwright report"} {
		if cond := find(name).keys["if"]; !strings.HasPrefix(cond, "always()") {
			t.Errorf("step %q if = %q, want prefix always()", name, cond)
		}
	}
	if v, ok := find("Topology (${{ matrix.shard }})").keys["continue-on-error"]; ok {
		t.Errorf("the topology test step must not set continue-on-error (got %q)", v)
	}
}

func TestDevEnvGateStillReadsTheE2EAndTopologyResults(t *testing.T) {
	j := devEnvJob(t, "e2e-gate")
	needs, _ := jobKey(j, "needs")
	for _, want := range []string{"e2e", "topology"} {
		if !strings.Contains(needs, want) {
			t.Errorf("e2e-gate needs %q lacks %q", needs, want)
		}
	}
	if cond, _ := jobKey(j, "if"); !strings.HasPrefix(cond, "always()") {
		t.Errorf("e2e-gate if = %q, want prefix always()", cond)
	}
	for _, s := range j.steps() {
		if !strings.Contains(s.keys["run"], "node e2e/mergeGate.ts") {
			continue
		}
		for k, v := range map[string]string{"E2E_RESULT": "${{ needs.e2e.result }}", "TOPOLOGY_RESULT": "${{ needs.topology.result }}"} {
			if s.env[k] != v {
				t.Errorf("gate env %s = %q, want %q", k, s.env[k], v)
			}
		}
		return
	}
	t.Fatal("e2e-gate has no node e2e/mergeGate.ts step")
}
