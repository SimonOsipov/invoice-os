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
		scrubAt, uploads, scrubRun := -1, 0, ""
		var paths []string
		for i, lines := range steps {
			block := strings.Join(lines, "\n")
			switch {
			case slices.ContainsFunc(lines, func(l string) bool { return strings.TrimSpace(l) == "id: scrub" }):
				scrubAt = i
				for _, l := range lines {
					if strings.HasPrefix(strings.TrimSpace(l), "run: ") {
						scrubRun = l
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
				for _, l := range lines {
					if p, ok := strings.CutPrefix(strings.TrimSpace(l), "path: "); ok {
						paths = append(paths, strings.TrimSuffix(p, "/"))
					}
				}
				if scrubAt < 0 {
					t.Errorf("%s: %q uploads before the scrub step", jobName, lines[0])
				}
				if !strings.Contains(block, "&& steps.scrub.outcome == 'success'") {
					t.Errorf("%s: %q is not gated on steps.scrub.outcome == 'success'", jobName, lines[0])
				}
			}
		}
		if scrubAt < 0 {
			t.Errorf("%s has no scrub step", jobName)
		}
		for _, p := range paths {
			if !slices.Contains(strings.Fields(scrubRun), p) {
				t.Errorf("%s: the scrub step %q does not scrub the uploaded %s", jobName, scrubRun, p)
			}
		}
		if len(paths) != uploads {
			t.Errorf("%s: %d upload paths read for %d uploads", jobName, len(paths), uploads)
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
		var scrub *step
		for i := range job.Steps {
			st := &job.Steps[i]
			if st.ID == "scrub" {
				scrub = st
				continue
			}
			if !strings.HasPrefix(st.Uses, "actions/upload-artifact@") || !uploadsOutput("path: "+st.With["path"]) {
				continue
			}
			uploads++
			path := strings.TrimSuffix(st.With["path"], "/")
			switch {
			case scrub == nil:
				t.Errorf("%s: %q uploads %s before any scrub step", name, st.Name, path)
			case !strings.HasPrefix(scrub.Run, "node e2e/reportScrub.ts ") || !slices.Contains(strings.Fields(scrub.Run), path) || scrub.ContinueOnError != nil:
				t.Errorf("%s: scrub step %q does not strictly scrub %s", name, scrub.Run, path)
			case !strings.Contains(st.If, "steps.scrub.outcome == 'success'"):
				t.Errorf("%s: %q is not gated on the scrub", name, st.Name)
			}
		}
	}
	if uploads != 10 {
		t.Errorf("%d Playwright uploads parsed, want 10", uploads)
	}
}
