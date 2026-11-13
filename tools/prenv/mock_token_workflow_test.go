package main

import (
	"slices"
	"strings"
	"testing"
)

func TestDevEnvMockIssuerToken_NotUnmaskedOrExportedToAnOutput(t *testing.T) {
	t.Parallel()
	const (
		read    = `preview.sh mock-token "$PR"`
		mask    = `::add-mask::$mock`
		export  = `MOCK_ISSUER_TOKEN=$mock" >>"$GITHUB_ENV"`
		nightly = "if: needs.changes.outputs.nightly == 'true'"
	)
	has := func(lines []string, sub string) bool {
		return slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, sub) })
	}
	first := func(steps [][]string, pred func([]string) bool) int {
		return slices.IndexFunc(steps, pred)
	}
	jobs := map[string]workflowJob{}
	yml := readWorkflow(t, "dev-env.yml")
	for _, j := range workflowJobsOf(yml) {
		jobs[j.name] = j
	}
	for _, name := range []string{"e2e", "topology", "nightly-ai"} {
		steps := rawSteps(jobs[name])
		var at []int
		for i, s := range steps {
			if has(s, read) {
				at = append(at, i)
			}
		}
		if len(at) != 1 {
			t.Errorf("%s: %d steps run %s, want 1", name, len(at), read)
			continue
		}
		s := steps[at[0]]
		text := strings.Join(s, "\n")
		m, e := strings.Index(text, mask), strings.Index(text, export)
		if m < 0 || e < 0 || m > e {
			t.Errorf("%s: %s must come before %s", name, mask, export)
		}
		if strings.Contains(text, "GITHUB_OUTPUT") || strings.Contains(text, "GITHUB_STEP_SUMMARY") {
			t.Errorf("%s: the token step writes to GITHUB_OUTPUT or GITHUB_STEP_SUMMARY", name)
		}
		if gated := has(s, "if:"); name == "nightly-ai" && gated || name != "nightly-ai" && !slices.Contains(s, nightly) {
			t.Errorf("%s: wrong if on the token step: %v", name, s)
		}
		install := first(steps, func(s []string) bool { return has(s, "pnpm install") })
		auth := first(steps, func(s []string) bool { return has(s, "uses: google-github-actions/auth") })
		spec := first(steps, func(s []string) bool { return has(s, "e2e-suite.sh") || has(s, "e2e nightly-ai") })
		if install < 0 || auth < 0 || spec < 0 || at[0] < install || at[0] < auth || at[0] > spec {
			t.Errorf("%s: token step %d must follow install %d and auth %d and precede the first spec step %d", name, at[0], install, auth, spec)
		}
	}
	for name, j := range jobs {
		if name == "e2e" || name == "topology" || name == "nightly-ai" {
			continue
		}
		text := strings.Join(stripHashComments(j.lines), "\n")
		if strings.Contains(text, "mock-token") || strings.Contains(text, "MOCK_ISSUER_TOKEN") {
			t.Errorf("job %s names mock-token or MOCK_ISSUER_TOKEN", name)
		}
	}
}
