package main

import (
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// uploadsOutput reports whether an upload step publishes a Playwright or nightly output.
func uploadsOutput(block string) bool {
	for _, p := range []string{"path: e2e/report-", "path: e2e/flaky-results/", "path: nightly-results/"} {
		if strings.Contains(block, p) {
			return true
		}
	}
	return false
}

// isScrubID reports whether a step id names a scrub step: `scrub` or `scrub-*`.
func isScrubID(id string) bool { return id == "scrub" || strings.HasPrefix(id, "scrub-") }

func TestDevEnvScrubsBeforeEveryPlaywrightUpload(t *testing.T) {
	t.Parallel()
	for _, jobName := range []string{"e2e", "topology"} {
		var steps [][]string
		for _, line := range devEnvJob(t, jobName).lines {
			if strings.HasPrefix(line, "      - ") {
				steps = append(steps, nil)
			}
			if len(steps) > 0 {
				steps[len(steps)-1] = append(steps[len(steps)-1], line)
			}
		}
		uploads := 0
		scrubs := map[string]string{} // scrub step id -> its run line, for scrubs seen so far
		for _, lines := range steps {
			block := strings.Join(lines, "\n")
			switch {
			case slices.ContainsFunc(lines, func(l string) bool {
				id, ok := strings.CutPrefix(strings.TrimSpace(l), "id: ")
				return ok && isScrubID(id)
			}):
				id := ""
				for _, l := range lines {
					if v, ok := strings.CutPrefix(strings.TrimSpace(l), "id: "); ok {
						id = v
					}
					if strings.HasPrefix(strings.TrimSpace(l), "run: ") {
						scrubs[id] = l
					}
				}
				if strings.Contains(block, "continue-on-error") {
					t.Errorf("%s: the scrub step must not set continue-on-error", jobName)
				}
				if !strings.Contains(block, "run: node e2e/reportScrub.ts ") || !strings.Contains(block, "if: always()") {
					t.Errorf("%s: the scrub step must run reportScrub under always()", jobName)
				}
			case strings.Contains(block, "uses: actions/upload-artifact@") && uploadsOutput(block):
				uploads++
				gated := false
				path := ""
				for _, l := range lines {
					if p, ok := strings.CutPrefix(strings.TrimSpace(l), "path: "); ok {
						path = strings.TrimSuffix(p, "/")
					}
				}
				covered := false
				for id, run := range scrubs {
					if slices.Contains(strings.Fields(run), path) {
						covered = true
						if strings.Contains(block, "&& steps."+id+".outcome == 'success'") {
							gated = true
						}
					}
				}
				switch {
				case path == "":
					t.Errorf("%s: no upload path read for %q", jobName, lines[0])
				case !covered:
					t.Errorf("%s: no earlier scrub step scrubs the uploaded %s (%q)", jobName, path, lines[0])
				case !gated:
					t.Errorf("%s: %q is not gated on the outcome of the scrub step that scrubs %s", jobName, lines[0], path)
				}
			}
		}
		if len(scrubs) == 0 {
			t.Errorf("%s has no scrub step", jobName)
		}
		if want := map[string]int{"e2e": 4, "topology": 3}[jobName]; uploads != want {
			t.Errorf("%s: %d gated uploads, want %d", jobName, uploads, want)
		}
	}
}

// TestEveryPlaywrightUploadFollowsAGatedScrub parses each workflow: an upload of Playwright
// output must come after a scrub step that covers its path, and be gated on that step's success.
func TestEveryPlaywrightUploadFollowsAGatedScrub(t *testing.T) {
	t.Parallel()
	type step struct {
		ID              string            `yaml:"id"`
		Name            string            `yaml:"name"`
		If              string            `yaml:"if"`
		Uses            string            `yaml:"uses"`
		Run             string            `yaml:"run"`
		With            map[string]string `yaml:"with"`
		ContinueOnError any               `yaml:"continue-on-error"`
	}
	var doc struct {
		Jobs map[string]struct {
			Steps []step `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(readWorkflow(t, "dev-env.yml")), &doc); err != nil {
		t.Fatal(err)
	}
	uploads := 0
	for name, job := range doc.Jobs {
		scrubs := map[string]*step{}
		for i := range job.Steps {
			st := &job.Steps[i]
			if isScrubID(st.ID) {
				scrubs[st.ID] = st
				continue
			}
			if !strings.HasPrefix(st.Uses, "actions/upload-artifact@") || !uploadsOutput("path: "+st.With["path"]) {
				continue
			}
			uploads++
			path := strings.TrimSuffix(st.With["path"], "/")
			strict, gated := false, false
			for id, sc := range scrubs {
				if strings.HasPrefix(sc.Run, "node e2e/reportScrub.ts ") && slices.Contains(strings.Fields(sc.Run), path) && sc.ContinueOnError == nil {
					strict = true
					gated = gated || strings.Contains(st.If, "steps."+id+".outcome == 'success'")
				}
			}
			switch {
			case !strict:
				t.Errorf("%s: no earlier scrub step strictly scrubs %s for %q", name, path, st.Name)
			case !gated:
				t.Errorf("%s: %q is not gated on the scrub step that scrubs %s", name, st.Name, path)
			}
		}
	}
	if uploads != 10 {
		t.Errorf("%d Playwright uploads parsed, want 10", uploads)
	}
}
