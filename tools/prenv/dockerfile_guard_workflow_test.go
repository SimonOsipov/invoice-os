package main

import (
	"slices"
	"strings"
	"testing"
)

func TestCIDockerfileGuardJobIsGatedByTheRollup(t *testing.T) {
	t.Parallel()
	job := ciJob(t, "dockerfile-guard")
	if v, _ := jobKey(job, "name"); v != "Dockerfile guard" {
		t.Errorf("dockerfile-guard name = %q, want Dockerfile guard", v)
	}
	var jobRun string
	for _, s := range job.steps() {
		jobRun += s.keys["run"] + "\n"
	}
	for _, want := range []string{
		"go run ./internal/tools/dockerguard",
		"go test ./internal/tools/dockerguard/",
	} {
		if !strings.Contains(jobRun, want) {
			t.Errorf("dockerfile-guard run lacks %q:\n%s", want, jobRun)
		}
	}

	ci := ciJob(t, "ci")
	if !slices.Contains(jobList(ci, "needs"), "dockerfile-guard") {
		t.Errorf("ci needs = %v, want dockerfile-guard in it", jobList(ci, "needs"))
	}
	var run string
	for _, s := range ci.steps() {
		run += s.keys["run"] + "\n"
	}
	const guard = `if [ "${{ needs.dockerfile-guard.result }}" != "success" ]; then`
	_, body, ok := strings.Cut(run, guard)
	if !ok {
		t.Fatalf("ci run lacks the != success guard for needs.dockerfile-guard.result:\n%s", run)
	}
	body, _, _ = strings.Cut(body, "\nfi")
	if !strings.Contains(body, "exit 1") {
		t.Errorf("dockerfile-guard guard does not exit 1:\n%s", body)
	}
}
