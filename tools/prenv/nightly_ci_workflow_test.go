// nightly_ci_workflow_test.go pins the full mode of ci.yml: a workflow_dispatch runs every job.
package main

import (
	"slices"
	"strings"
	"testing"
)

const (
	ciDispatch   = "github.event_name == 'workflow_dispatch'"
	ciGroupLine  = "group: ci-${{ github.workflow }}-${{ " + ciDispatch + " && 'nightly-' || '' }}${{ github.ref }}"
	ciNameLine   = "${{ " + ciDispatch + " && 'CI (nightly)' || 'CI' }}"
	ciLaneOutput = "lane: ${{ steps.lane.outputs.lane }}"
)

var ciForcedOutputs = []string{"go", "frontend", "migrations", "sidecar", "idp", "hooks", "caddy"}

func ciForcedOutputLine(name string) string {
	return name + ": ${{ " + ciDispatch + " && 'true' || steps.filter.outputs." + name + " }}"
}

func ciJobOf(yaml, id string) (workflowJob, bool) {
	for _, j := range workflowJobsOf(yaml) {
		if j.name == id {
			return j, true
		}
	}
	return workflowJob{}, false
}

// ciFullModeProblems names each rule of ci.yml full mode that the file breaks.
func ciFullModeProblems(yaml string) []string {
	lines := strings.Split(yaml, "\n")
	var p []string

	on := onBlockLines(lines)
	if !slices.Contains(on, "workflow_dispatch:") {
		p = append(p, "on: lacks workflow_dispatch")
	}
	for _, l := range on {
		if !slices.Contains([]string{"pull_request:", "push:", "workflow_dispatch:"}, l) {
			p = append(p, "on: names "+l)
		}
	}
	if len(blockLines(lines, 2, "workflow_dispatch")) > 0 {
		p = append(p, "workflow_dispatch has inputs")
	}
	if !slices.Equal(blockLines(lines, 2, "push"), []string{"branches: [main]"}) {
		p = append(p, "on: push is not branches [main]")
	}

	conc := blockLines(lines, 0, "concurrency")
	if !slices.Contains(conc, ciGroupLine) {
		p = append(p, "concurrency group has no dispatch part")
	}
	if !slices.Contains(conc, "cancel-in-progress: true") {
		p = append(p, "concurrency cancel-in-progress is not true")
	}

	changes, ok := ciJobOf(yaml, "changes")
	if !ok {
		return append(p, "no changes job")
	}
	outs := blockLines(changes.lines, 4, "outputs")
	for _, name := range ciForcedOutputs {
		if !slices.Contains(outs, ciForcedOutputLine(name)) {
			p = append(p, "changes output \""+name+"\" is not forced on dispatch")
		}
	}
	if !slices.Contains(outs, ciLaneOutput) {
		p = append(p, "changes output \"lane\" changed")
	}
	filterSkipped := false
	for i, l := range changes.lines {
		if strings.TrimSpace(l) != "- uses: dorny/paths-filter@v3" {
			continue
		}
		for _, n := range changes.lines[i+1:] {
			if strings.HasPrefix(n, "      - ") {
				break
			}
			if strings.TrimSpace(n) == "if: github.event_name != 'workflow_dispatch'" {
				filterSkipped = true
			}
		}
	}
	if !filterSkipped {
		p = append(p, "the paths-filter step runs on a dispatch")
	}

	for _, lane := range []string{"library", "blog"} {
		j, ok := ciJobOf(yaml, lane)
		if !ok {
			p = append(p, "no "+lane+" job")
			continue
		}
		if v, _ := jobKey(j, "if"); v != "needs.changes.outputs.lane == '"+lane+"' || "+ciDispatch {
			p = append(p, lane+" job does not run on a dispatch")
		}
	}

	if j, ok := ciJobOf(yaml, "ci"); !ok {
		p = append(p, "no ci job")
	} else if v, _ := jobKey(j, "name"); v != ciNameLine {
		p = append(p, "the ci roll-up name is not gated on a dispatch")
	}
	return p
}

func requireCIFullModeProblems(t *testing.T, yml string, want ...string) {
	t.Helper()
	got := ciFullModeProblems(yml)
	if len(got) != len(want) {
		t.Fatalf("problems = %q, want %d naming %q", got, len(want), want)
	}
	for i, w := range want {
		if !strings.Contains(got[i], w) {
			t.Errorf("problem %d = %q, want it to name %q", i, got[i], w)
		}
	}
}

func TestCIFullMode_RealFileHasNoProblems(t *testing.T) {
	t.Parallel()
	if got := ciFullModeProblems(readWorkflow(t, "ci.yml")); len(got) != 0 {
		t.Fatalf("ci.yml full mode problems: %q", got)
	}
}

func TestCIFullMode_DispatchTriggerRemovedFails(t *testing.T) {
	t.Parallel()
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), "  workflow_dispatch:\n", "")
	requireCIFullModeProblems(t, yml, "on: lacks workflow_dispatch")
}

func TestCIFullMode_DispatchInputsFail(t *testing.T) {
	t.Parallel()
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), "  workflow_dispatch:\n",
		"  workflow_dispatch:\n    inputs:\n      x:\n        type: string\n")
	requireCIFullModeProblems(t, yml, "workflow_dispatch has inputs")
}

func TestCIFullMode_PushBranchWidenedFails(t *testing.T) {
	t.Parallel()
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), "    branches: [main]\n", "    branches: [main, epic/*]\n")
	requireCIFullModeProblems(t, yml, "push")
}

func TestCIFullMode_ExtraTriggerFails(t *testing.T) {
	t.Parallel()
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), "  workflow_dispatch:\n", "  workflow_dispatch:\n  schedule:\n")
	requireCIFullModeProblems(t, yml, "on: names schedule:")
}

func TestCIFullMode_ConcurrencyWithoutDispatchPartFails(t *testing.T) {
	t.Parallel()
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), ciGroupLine, "group: ci-${{ github.workflow }}-${{ github.ref }}")
	requireCIFullModeProblems(t, yml, "concurrency group has no dispatch part")
}

func TestCIFullMode_ConcurrencyCancelFalseFails(t *testing.T) {
	t.Parallel()
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), "cancel-in-progress: true", "cancel-in-progress: false")
	requireCIFullModeProblems(t, yml, "cancel-in-progress")
}

func TestCIFullMode_EachOutputForced(t *testing.T) {
	t.Parallel()
	for _, name := range ciForcedOutputs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			yml := mutateOnce(t, readWorkflow(t, "ci.yml"), "      "+ciForcedOutputLine(name)+"\n",
				"      "+name+": ${{ steps.filter.outputs."+name+" }}\n")
			requireCIFullModeProblems(t, yml, "changes output \""+name+"\"")
		})
	}
}

func TestCIFullMode_LaneOutputChangedFails(t *testing.T) {
	t.Parallel()
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"), "      "+ciLaneOutput+"\n",
		"      lane: ${{ "+ciDispatch+" && 'true' || steps.lane.outputs.lane }}\n")
	requireCIFullModeProblems(t, yml, "\"lane\"")
}

func TestCIFullMode_FilterStepRunsOnDispatchFails(t *testing.T) {
	t.Parallel()
	yml := mutateOnce(t, readWorkflow(t, "ci.yml"),
		"        id: filter\n        if: github.event_name != 'workflow_dispatch'\n", "        id: filter\n")
	requireCIFullModeProblems(t, yml, "paths-filter")
}

func TestCIFullMode_LibraryAndBlogRunOnDispatch(t *testing.T) {
	t.Parallel()
	for _, lane := range []string{"library", "blog"} {
		t.Run(lane, func(t *testing.T) {
			t.Parallel()
			yml := mutateOnce(t, readWorkflow(t, "ci.yml"),
				"    if: needs.changes.outputs.lane == '"+lane+"' || "+ciDispatch+"\n",
				"    if: needs.changes.outputs.lane == '"+lane+"'\n")
			requireCIFullModeProblems(t, yml, lane+" job")
		})
	}
}

func TestCIFullMode_RollupNameNotDispatchGatedFails(t *testing.T) {
	t.Parallel()
	ci := readWorkflow(t, "ci.yml")
	requireCIFullModeProblems(t, mutateOnce(t, ci, "    name: "+ciNameLine+"\n", "    name: CI\n"), "roll-up")
	swapped := "${{ " + ciDispatch + " && 'CI' || 'CI (nightly)' }}"
	requireCIFullModeProblems(t, mutateOnce(t, ci, ciNameLine, swapped), "roll-up")
}
