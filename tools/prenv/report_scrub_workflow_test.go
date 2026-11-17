package main

import (
	"slices"
	"strings"
	"testing"
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
			case strings.Contains(block, "id: scrub"):
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
