package main

import (
	"slices"
	"strings"
	"testing"
)

func TestCIContractStepJobIsGatedByTheRollup(t *testing.T) {
	t.Parallel()
	job := ciJob(t, "contract-step")
	if v, _ := jobKey(job, "name"); v != "Schema deletion" {
		t.Errorf("contract-step name = %q, want Schema deletion", v)
	}
	var jobRun string
	for _, s := range job.steps() {
		jobRun += s.keys["run"] + "\n"
	}
	for _, want := range []string{
		"go run ./internal/tools/contractstep",
		"go test ./internal/tools/contractstep/",
		`if [ "${{ github.event_name }}" = "workflow_dispatch" ]; then BASE=HEAD; fi`,
	} {
		if !strings.Contains(jobRun, want) {
			t.Errorf("contract-step run lacks %q:\n%s", want, jobRun)
		}
	}

	ci := ciJob(t, "ci")
	if !slices.Contains(jobList(ci, "needs"), "contract-step") {
		t.Errorf("ci needs = %v, want contract-step in it", jobList(ci, "needs"))
	}
	var run string
	for _, s := range ci.steps() {
		run += s.keys["run"] + "\n"
	}
	const guard = `if [ "${{ needs.contract-step.result }}" != "success" ]; then`
	_, body, ok := strings.Cut(run, guard)
	if !ok {
		t.Fatalf("ci run lacks the != success guard for needs.contract-step.result:\n%s", run)
	}
	body, _, _ = strings.Cut(body, "\nfi")
	if !strings.Contains(body, "exit 1") {
		t.Errorf("contract-step guard does not exit 1:\n%s", body)
	}
}
