// lane_workflow_test.go pins the small-lane wiring of .github/workflows/ci.yml and dev-env.yml.
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
	t.Parallel()
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
	t.Parallel()
	fe := ciJob(t, "frontend")
	const feWant = "needs.changes.outputs.frontend == 'true' && needs.changes.outputs.lane != 'library' && needs.changes.outputs.lane != 'blog'"
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
	if v, _ := jobKey(lib, "if"); v != "needs.changes.outputs.lane == 'library' || github.event_name == 'workflow_dispatch'" {
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

func TestCIBlogJobReplacesFrontendOnTheSmallLane(t *testing.T) {
	fe := ciJob(t, "frontend")
	const feWant = "needs.changes.outputs.frontend == 'true' && needs.changes.outputs.lane != 'library' && needs.changes.outputs.lane != 'blog'"
	if feIf, _ := jobKey(fe, "if"); feIf != feWant {
		t.Errorf("frontend if = %q, want %q", feIf, feWant)
	}
	j := ciJob(t, "blog")
	if v, _ := jobKey(j, "name"); v != "Blog" {
		t.Errorf("blog name = %q, want Blog", v)
	}
	if got := jobList(j, "needs"); got != nil {
		t.Errorf("blog needs list = %v, want the scalar form", got)
	}
	if v, _ := jobKey(j, "needs"); v != "changes" {
		t.Errorf("blog needs = %q, want changes", v)
	}
	if v, _ := jobKey(j, "if"); v != "needs.changes.outputs.lane == 'blog' || github.event_name == 'workflow_dispatch'" {
		t.Errorf("blog if = %q", v)
	}
	var runs []string
	steps := j.steps()
	if len(steps) == 0 {
		t.Fatal("blog job has no steps")
	}
	for _, s := range steps {
		for _, k := range []string{"if", "continue-on-error"} {
			if v, ok := s.keys[k]; ok {
				t.Errorf("blog step %v has %s: %q, want none", s.keys, k, v)
			}
		}
		if r := s.keys["run"]; r != "" {
			runs = append(runs, r)
		}
	}
	for _, want := range []string{
		"pnpm --filter @invoice-os/blog test",
		"pnpm --filter @invoice-os/blog build",
		"pnpm --filter @invoice-os/monitoring test",
		"pnpm --filter @invoice-os/e2e test:unit",
	} {
		if !slices.Contains(runs, want) {
			t.Errorf("blog steps lack %q; runs = %v", want, runs)
		}
	}
}

func TestCIRollupRequiresTheBlogJob(t *testing.T) {
	ci := ciJob(t, "ci")
	if !slices.Contains(jobList(ci, "needs"), "blog") {
		t.Errorf("ci needs = %v, want blog in it", jobList(ci, "needs"))
	}
	var run string
	for _, s := range ci.steps() {
		run += s.keys["run"] + "\n"
	}
	const guard = `if [ "${{ needs.blog.result }}" = "failure" ] || [ "${{ needs.blog.result }}" = "cancelled" ]; then` +
		"\n" + `echo "::error::Blog job failed"; exit 1` + "\n" + "fi"
	if !strings.Contains(run, guard) {
		t.Errorf("ci run lacks the failure-or-cancelled exit for needs.blog.result:\n%s", run)
	}
	if !strings.Contains(run, `echo "blog:          ${{ needs.blog.result }}"`) {
		t.Errorf("ci run does not echo needs.blog.result:\n%s", run)
	}
}

func TestCIRollupRequiresTheLibraryJob(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	for _, id := range []string{"clean-clone", "stale-refs", "migration-order", "cite-refs"} {
		if v, ok := jobKey(ciJob(t, id), "if"); ok {
			t.Errorf("%s has if: %q, want none", id, v)
		}
	}
	if _, ok := jobKey(ciJob(t, "frontend"), "if"); !ok {
		t.Error("control: frontend has no if:, so the scan cannot tell a conditional job")
	}
}

// rawSteps splits a job's steps into their trimmed lines.
func rawSteps(j workflowJob) [][]string {
	var out [][]string
	in := false
	for _, l := range stripHashComments(j.lines) {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if !in {
			in = l == "    steps:"
			continue
		}
		t := strings.TrimSpace(l)
		if strings.HasPrefix(l, "      - ") {
			out = append(out, nil)
			t = strings.TrimPrefix(t, "- ")
		}
		if len(out) > 0 {
			out[len(out)-1] = append(out[len(out)-1], t)
		}
	}
	return out
}

func TestDevEnvE2EFilterKeepsEveryFrontendPackage(t *testing.T) {
	t.Parallel()
	var filter []string
	for _, s := range rawSteps(devEnvJob(t, "changes")) {
		if strings.HasPrefix(s[0], "uses: dorny/paths-filter@") {
			filter = s
		}
	}
	if filter == nil {
		t.Fatal("changes has no paths-filter step")
	}
	for _, want := range []string{"- 'frontend/**'", "- 'internal/**'"} {
		if !slices.Contains(filter, want) {
			t.Errorf("e2e filter lacks %s", want)
		}
	}
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
