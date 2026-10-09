// lane_workflow_test.go pins the small-lane wiring of .github/workflows/ci.yml.
package main

import (
	"slices"
	"strings"
	"testing"
)

func ciJob(t *testing.T, id string) workflowJob {
	t.Helper()
	for _, j := range workflowJobsOf(readWorkflow(t, "ci.yml")) {
		if j.name == id {
			return j
		}
	}
	t.Fatalf("ci.yml has no job %q", id)
	return workflowJob{}
}

func TestCIChangesPublishesThePRLane(t *testing.T) {
	j := ciJob(t, "changes")
	if !slices.Contains(blockLines(j.lines, 4, "outputs"), "lane: ${{ steps.lane.outputs.lane }}") {
		t.Errorf("changes outputs lack lane: ${{ steps.lane.outputs.lane }}")
	}
	laneSteps := 0
	for _, s := range j.steps() {
		if !strings.Contains(s.keys["run"], "scripts/ci/lane.sh") {
			continue
		}
		laneSteps++
		if s.keys["run"] != "bash scripts/ci/lane.sh pr" || s.keys["id"] != "lane" {
			t.Errorf("lane step run=%q id=%q, want bash scripts/ci/lane.sh pr / lane", s.keys["run"], s.keys["id"])
		}
		if s.keys["if"] != "github.event_name == 'pull_request'" {
			t.Errorf("lane step if = %q, want the pull_request gate", s.keys["if"])
		}
	}
	if laneSteps != 1 {
		t.Fatalf("changes has %d lane.sh steps, want 1", laneSteps)
	}
	checkout, depth, lane := -1, -1, -1
	for i, l := range j.lines {
		tl := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(tl, "- uses: actions/checkout@") && checkout < 0:
			checkout = i
		case tl == "fetch-depth: 2" && depth < 0:
			depth = i
		case strings.Contains(tl, "scripts/ci/lane.sh"):
			lane = i
		}
	}
	if checkout < 0 || depth != checkout+2 || lane < depth {
		t.Errorf("want checkout with fetch-depth: 2 before the lane step; checkout=%d depth=%d lane=%d", checkout, depth, lane)
	}
}

func TestCILibraryJobReplacesFrontendOnTheSmallLane(t *testing.T) {
	fe := ciJob(t, "frontend")
	const feWant = "needs.changes.outputs.frontend == 'true' && needs.changes.outputs.lane != 'library'"
	if feIf, _ := jobKey(fe, "if"); feIf != feWant {
		t.Errorf("frontend if = %q, want %q", feIf, feWant)
	}
	lib := ciJob(t, "library")
	if v, _ := jobKey(lib, "name"); v != "Library" {
		t.Errorf("library name = %q, want Library", v)
	}
	if got := jobList(lib, "needs"); got != nil {
		t.Errorf("library needs list = %v, want the scalar form", got)
	}
	if v, _ := jobKey(lib, "needs"); v != "changes" {
		t.Errorf("library needs = %q, want changes", v)
	}
	if v, _ := jobKey(lib, "if"); v != "needs.changes.outputs.lane == 'library'" {
		t.Errorf("library if = %q", v)
	}
	var runs []string
	libSteps := lib.steps()
	if len(libSteps) == 0 {
		t.Fatal("library job has no steps")
	}
	for _, s := range libSteps {
		for _, k := range []string{"if", "continue-on-error"} {
			if v, ok := s.keys[k]; ok {
				t.Errorf("library step %v has %s: %q, want none", s.keys, k, v)
			}
		}
		if r := s.keys["run"]; r != "" {
			runs = append(runs, r)
		}
	}
	for _, want := range []string{
		"pnpm --filter @invoice-os/library test",
		"pnpm --filter @invoice-os/library build",
		"pnpm --filter @invoice-os/monitoring test",
		"pnpm --filter @invoice-os/e2e test:unit",
	} {
		if !slices.Contains(runs, want) {
			t.Errorf("library steps lack %q; runs = %v", want, runs)
		}
	}
}

func TestCIRollupRequiresTheLibraryJob(t *testing.T) {
	ci := ciJob(t, "ci")
	if !slices.Contains(jobList(ci, "needs"), "library") {
		t.Errorf("ci needs = %v, want library in it", jobList(ci, "needs"))
	}
	var run string
	for _, s := range ci.steps() {
		run += s.keys["run"] + "\n"
	}
	const guard = `if [ "${{ needs.library.result }}" = "failure" ] || [ "${{ needs.library.result }}" = "cancelled" ]; then` +
		"\n" + `echo "::error::Library job failed"; exit 1` + "\n" + "fi"
	if !strings.Contains(run, guard) {
		t.Errorf("ci run lacks the failure-or-cancelled exit for needs.library.result:\n%s", run)
	}
}

func TestCIStaticChecksStayUnconditional(t *testing.T) {
	for _, id := range []string{"clean-clone", "stale-refs", "migration-order", "cite-refs"} {
		if v, ok := jobKey(ciJob(t, id), "if"); ok {
			t.Errorf("%s has if: %q, want none", id, v)
		}
	}
	if _, ok := jobKey(ciJob(t, "frontend"), "if"); !ok {
		t.Error("control: frontend has no if:, so the scan cannot tell a conditional job")
	}
}
