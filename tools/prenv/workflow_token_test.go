// workflow_token_test.go pins that no trusted trigger, inherited secret or local action reaches a
// workflow's credentials.
package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

var (
	trustedOnRE    = regexp.MustCompile(`(?i)\b(pull_request_target|workflow_run|workflow_call)\b`)
	secretsInherit = regexp.MustCompile(`^secrets:\s*['"]?inherit['"]?\s*$`)
	localUses      = regexp.MustCompile(`^uses:\s*['"]?\./`)
)

var onKeyRE = regexp.MustCompile(`^(on|'on'|"on"):`)

// onBlockLines returns the comment-stripped inline value of `on:` and its direct child lines.
func onBlockLines(lines []string) []string {
	var out []string
	in, childIndent := false, -1
	for _, l := range stripHashComments(lines) {
		if strings.TrimSpace(l) == "" {
			continue
		}
		cur := len(l) - len(strings.TrimLeft(l, " "))
		if cur == 0 {
			in, childIndent = false, -1
			if loc := onKeyRE.FindStringIndex(l); loc != nil {
				in = true
				if rest := strings.TrimSpace(l[loc[1]:]); rest != "" {
					out = append(out, rest)
				}
			}
			continue
		}
		if in && (childIndent < 0 || cur == childIndent) {
			childIndent = cur
			out = append(out, strings.TrimSpace(l))
		}
	}
	return out
}

func trustedTriggerFaults(yaml string) []string {
	lines := strings.Split(yaml, "\n")
	var faults []string
	for _, l := range onBlockLines(lines) {
		if m := trustedOnRE.FindString(l); m != "" {
			faults = append(faults, "on: names "+m)
		}
	}
	for _, l := range stripHashComments(lines) {
		t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "- "))
		switch {
		case secretsInherit.MatchString(t):
			faults = append(faults, "secrets: inherit")
		case localUses.MatchString(t):
			faults = append(faults, "local "+t)
		}
	}
	return faults
}

// workflowTexts reads every workflow file on disk, keyed by file name.
func workflowTexts(t *testing.T) (names []string, texts map[string]string) {
	t.Helper()
	dir := filepath.Join(repoRoot(t), ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	texts = map[string]string{}
	for _, e := range entries {
		if ext := filepath.Ext(e.Name()); ext == ".yml" || ext == ".yaml" {
			names = append(names, e.Name())
			texts[e.Name()] = readWorkflow(t, e.Name())
		}
	}
	sort.Strings(names)
	return names, texts
}

var knownWorkflows = []string{"ci.yml", "dev-env-sweeper.yml", "dev-env-teardown.yml", "dev-env.yml", "idp-release-watch.yml"}

func requireScanned(t *testing.T, names []string) {
	t.Helper()
	for _, want := range knownWorkflows {
		if !slices.Contains(names, want) {
			t.Fatalf("control: the scan did not cover %s (covered %v)", want, names)
		}
	}
}

func TestNoWorkflowReachesSecretsThroughATrustedTrigger(t *testing.T) {
	t.Parallel()
	names, texts := workflowTexts(t)
	requireScanned(t, names)
	if on := strings.Join(onBlockLines(strings.Split(texts["dev-env.yml"], "\n")), "\n"); !regexp.MustCompile(`\bpull_request\b`).MatchString(on) {
		t.Fatalf("control: the on: block of dev-env.yml names no pull_request: %q", on)
	}
	for _, name := range names {
		for _, f := range trustedTriggerFaults(texts[name]) {
			t.Errorf("%s: %s", name, f)
		}
	}
}
