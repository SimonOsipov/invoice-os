// action_pins_test.go pins that every workflow action is referenced by full commit SHA with a version comment.
package main

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var (
	// pinnedUses: owner/repo[/path]@<full commit>; the version is checked from the comment.
	pinnedUses     = regexp.MustCompile(`^uses:\s*['"]?[\w.-]+/[\w.-]+(/[\w./-]+)?@[0-9a-f]{40}['"]?$`)
	versionComment = regexp.MustCompile(`^#\s*v\d+\.\d+\.\d+$`)
)

// usesLine is one `uses:` key line: its number, comment-stripped value and trailing comment.
type usesLine struct {
	num           int
	text, comment string
}

func usesKeyLines(yaml string) []usesLine {
	raw := strings.Split(yaml, "\n")
	var out []usesLine
	for i, l := range stripHashComments(raw) {
		if t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "- ")); strings.HasPrefix(t, "uses:") && !localUses.MatchString(t) {
			out = append(out, usesLine{i + 1, t, strings.TrimSpace(raw[i][len(l):])})
		}
	}
	return out
}

// actionPinFaults returns one fault per `uses:` line that is not a full-SHA pin with a `# vX.Y.Z` comment.
func actionPinFaults(yaml string) []string {
	var faults []string
	for _, u := range usesKeyLines(yaml) {
		ref := strings.TrimSpace(strings.TrimPrefix(u.text, "uses:"))
		switch {
		case !pinnedUses.MatchString(u.text):
			faults = append(faults, fmt.Sprintf("line %d: %s: not pinned by a 40-char lowercase commit SHA", u.num, ref))
		case !versionComment.MatchString(u.comment):
			faults = append(faults, fmt.Sprintf("line %d: %s: missing the trailing # vX.Y.Z comment", u.num, ref))
		}
	}
	return faults
}

func TestActionPinFaults(t *testing.T) {
	t.Parallel()
	sha := "11d5960a326750d5838078e36cf38b85af677262"
	cases := []struct {
		name  string
		yaml  string
		lines []int  // expected fault line numbers
		names string // text every fault must contain
	}{
		{"major_tag", "      - uses: actions/checkout@v4", []int{1}, "actions/checkout@v4"},
		{"exact_tag", "      - name: Install uv\n        uses: astral-sh/setup-uv@v10.3.3", []int{2}, "astral-sh/setup-uv@v10.3.3"},
		{"branch", "- uses: actions/checkout@main", []int{1}, "actions/checkout@main"},
		{"short_sha", "- uses: actions/checkout@11d5960 # v4.4.0", []int{1}, "@11d5960"},
		{"sha_length_39_41", "- uses: actions/checkout@" + sha[1:] + " # v4.4.0\n- uses: actions/checkout@" + sha + "a # v4.4.0", []int{1, 2}, "commit SHA"},
		{"uppercase_sha", "- uses: actions/checkout@" + strings.ToUpper(sha) + " # v4.4.0", []int{1}, "commit SHA"},
		{"no_version_comment", "- uses: actions/checkout@" + sha, []int{1}, "# vX.Y.Z"},
		{"major_only_comment", "- uses: actions/checkout@" + sha + " # v4", []int{1}, "# vX.Y.Z"},
		{"docker_ref", "- uses: docker://alpine:3.20", []int{1}, "docker://alpine:3.20"},
		{"pinned_forms_pass", "- uses: actions/checkout@" + sha + " # v4.4.0\n  - name: Auth\n    uses: 'google-github-actions/auth@" + sha + "' # v3.0.0\n- uses: github/codeql-action/init@" + sha + " # v3.29.1", nil, ""},
		{"comment_and_run_lines_ignored", "# - uses: actions/checkout@v4\n      - run: echo uses: actions/checkout@v4\n      - name: uses: x@v1", nil, ""},
		{"local_skipped", "- uses: ./.github/actions/x", nil, ""},
		{"each_fault_reported", "- uses: actions/checkout@v4\n- uses: actions/checkout@" + sha + " # v4.4.0\n- uses: actions/setup-go@v7", []int{1, 3}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := actionPinFaults(c.yaml)
			if len(got) != len(c.lines) {
				t.Fatalf("faults = %q, want lines %v", got, c.lines)
			}
			for i, f := range got {
				if !strings.HasPrefix(f, fmt.Sprintf("line %d: ", c.lines[i])) || !strings.Contains(f, c.names) {
					t.Errorf("fault %q: want line %d naming %q", f, c.lines[i], c.names)
				}
			}
		})
	}
}

func TestEveryActionIsPinnedByCommit(t *testing.T) {
	t.Parallel()
	names, texts := workflowTexts(t)
	requireScanned(t, names)
	for _, name := range []string{"deploy-prod.yml", "nightly.yml", "rollback-prod.yml"} {
		if !slices.ContainsFunc(usesKeyLines(texts[name]), func(u usesLine) bool { return pinnedUses.MatchString(u.text) }) {
			t.Fatalf("control: %s yields no pinned uses: line", name)
		}
	}
	for _, name := range names {
		for _, f := range actionPinFaults(texts[name]) {
			t.Errorf("%s: %s", name, f)
		}
	}
}
