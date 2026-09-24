package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var releaseWatchYAML = filepath.Join("..", "..", "..", ".github", "workflows", "idp-release-watch.yml")

const issueTitle = "supabase/auth patch due"

func readWatch(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(releaseWatchYAML)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(string(raw), "\n")
}

func indentOf(l string) int { return len(l) - len(strings.TrimLeft(l, " ")) }

// block returns the non-blank lines nested under the first line whose trimmed text is key.
func block(lines []string, key string) []string {
	for i, l := range lines {
		if strings.TrimSpace(l) != key {
			continue
		}
		var out []string
		for _, m := range lines[i+1:] {
			if strings.TrimSpace(m) == "" {
				continue
			}
			if indentOf(m) <= indentOf(l) {
				break
			}
			out = append(out, m)
		}
		return out
	}
	return nil
}

// step returns the lines of the job step named name, its `- name:` line first.
func step(t *testing.T, lines []string, name string) []string {
	t.Helper()
	for i, l := range lines {
		if strings.TrimSpace(l) != "- name: "+name {
			continue
		}
		out := []string{l}
		for _, m := range lines[i+1:] {
			if strings.TrimSpace(m) != "" && indentOf(m) <= indentOf(l) {
				break
			}
			out = append(out, m)
		}
		return out
	}
	t.Fatalf("idp-release-watch.yml has no step named %q", name)
	return nil
}

// runScript returns the dedented `run: |` body of a step.
func runScript(t *testing.T, st []string) string {
	t.Helper()
	body := block(st, "run: |")
	if len(body) == 0 {
		t.Fatalf("step %q has no `run: |` block", strings.TrimSpace(st[0]))
	}
	cut := indentOf(body[0])
	var b strings.Builder
	for _, l := range body {
		b.WriteString(l[min(cut, indentOf(l)):] + "\n")
	}
	return b.String()
}

func stepField(st []string, field string) string {
	for _, l := range st[1:] {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), field+":"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func trimmed(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.TrimSpace(l)
	}
	return out
}

func TestReleaseWatchTriggersAndPermissions(t *testing.T) {
	lines := readWatch(t)

	on := trimmed(block(lines, "on:"))
	if len(on) == 0 {
		t.Fatal("no `on:` block; the scan is broken")
	}
	for _, want := range []string{"schedule:", "workflow_dispatch:", "pull_request:"} {
		if !slices.Contains(on, want) {
			t.Errorf("on: lacks %s (got %q)", want, on)
		}
	}
	if !slices.ContainsFunc(on, func(l string) bool { return strings.HasPrefix(l, "- cron: '") }) {
		t.Errorf("on.schedule has no cron entry (got %q)", on)
	}
	paths := trimmed(block(lines, "paths:"))
	wantPaths := []string{
		"- '.github/workflows/idp-release-watch.yml'",
		"- 'sidecar/auth/Dockerfile'",
		"- 'internal/tools/idppin/**'",
	}
	if !slices.Equal(paths, wantPaths) {
		t.Errorf("pull_request paths = %q, want %q", paths, wantPaths)
	}

	perms := trimmed(block(lines, "permissions:"))
	if want := []string{"contents: read", "issues: write"}; !slices.Equal(perms, want) {
		t.Errorf("permissions = %q, want exactly %q", perms, want)
	}
}

// `go run` turns idppin's exit 2 into 1, the mismatch code.
func TestReleaseWatchRunsTheBuiltIdppin(t *testing.T) {
	lines := readWatch(t)
	script := runScript(t, step(t, lines, "Compare the pin with the latest release and published advisories"))
	if !strings.Contains(script, `"$RUNNER_TEMP/idppin" latest-check sidecar/auth/Dockerfile "$latest"`) {
		t.Errorf("compare step does not pass the latest tag to the built idppin latest-check:\n%s", script)
	}
	for i, l := range lines {
		if strings.Contains(l, "go run") && !strings.HasPrefix(strings.TrimSpace(l), "#") {
			t.Errorf("idp-release-watch.yml line %d runs `go run`: %s", i+1, strings.TrimSpace(l))
		}
	}
}

func TestReleaseWatchIssueStepRunsOnAnyScheduledCompareFailure(t *testing.T) {
	lines := readWatch(t)
	if got := stepField(step(t, lines, "Compare the pin with the latest release and published advisories"), "id"); got != "compare" {
		t.Errorf("compare step id = %q, want compare", got)
	}
	st := step(t, lines, "Open or update the patch-due issue")
	cond := stepField(st, "if")
	for _, want := range []string{"failure()", "steps.compare.outcome == 'failure'", "github.event_name != 'pull_request'"} {
		if !strings.Contains(cond, want) {
			t.Errorf("issue step if: %q lacks %q", cond, want)
		}
	}
	if strings.Contains(cond, "finding") {
		t.Errorf("issue step if: %q gates on the finding output, so an API error opens no issue", cond)
	}
	if strings.Contains(cond, "||") {
		t.Errorf("issue step if: %q has an ||, which can bypass the pull_request guard", cond)
	}
	env := trimmed(block(st, "env:"))
	if !slices.Contains(env, "FINDING: ${{ steps.compare.outputs.finding }}") {
		t.Errorf("issue step env = %q, want FINDING from steps.compare.outputs.finding", env)
	}
}

// ghStub answers `gh` from fixture files in $STUB and logs each call to $STUB/calls.
const ghStub = `#!/usr/bin/env bash
printf '%s\n' "$*" >> "$STUB/calls"
jqexpr="" paginate="" state=""
args=("$@")
for ((i = 0; i < ${#args[@]}; i++)); do
  case "${args[i]}" in
    --jq) jqexpr="${args[i+1]}" ;;
    --paginate) paginate=1 ;;
    --state) state="${args[i+1]}" ;;
  esac
done
case "$*" in
  *releases/tags/*) f=tag.json ;;
  *releases/latest*) f=latest.json ;;
  *security-advisories*) f=advisories.json ;;
  "issue list"*) f=issues.json ;;
  "issue create"*|"issue reopen"*|"issue edit"*) exit 0 ;;
  *) echo "stub gh: unexpected call: $*" >&2; exit 9 ;;
esac
[ -f "$STUB/$f" ] || { echo "stub gh: HTTP 404 ($f)" >&2; exit 1; }
if [ "$f" = advisories.json ] && [ -z "$paginate" ]; then head -n 1 "$STUB/$f"; exit 0; fi
if [ "$f" = issues.json ] && [ "$state" = open ]; then jq -c 'map(select(.state == "OPEN"))' "$STUB/$f"; exit 0; fi
if [ -n "$jqexpr" ]; then jq -r "$jqexpr" "$STUB/$f"; else cat "$STUB/$f"; fi
`

type watchRun struct {
	exit          int
	out, body     string
	ghOut, ghCall string
}

// runWatchStep runs one step's script in a fake runner: stub gh, the built idppin, a fixture Dockerfile.
func runWatchStep(t *testing.T, stepName, dockerfile string, fixtures map[string]string, env ...string) watchRun {
	t.Helper()
	script := runScript(t, step(t, readWatch(t), stepName))

	work := t.TempDir()
	stub := filepath.Join(work, "stub")
	bin := filepath.Join(work, "bin")
	runner := filepath.Join(work, "runner")
	for _, d := range []string{stub, bin, runner, filepath.Join(work, "sidecar", "auth")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, s string, mode os.FileMode) {
		if err := os.WriteFile(p, []byte(s), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(work, "sidecar", "auth", "Dockerfile"), dockerfile, 0o644)
	write(filepath.Join(bin, "gh"), ghStub, 0o755)
	idp, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(runner, "idppin"), string(idp), 0o755)
	for name, body := range fixtures {
		p := filepath.Join(stub, name)
		if name == "finding.md" {
			p = filepath.Join(runner, name)
		}
		write(p, body, 0o644)
	}
	output := filepath.Join(work, "github_output")
	write(output, "", 0o644)

	cmd := exec.Command("bash", "-e", "-c", script)
	cmd.Dir = work
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"STUB="+stub, "RUNNER_TEMP="+runner, "GITHUB_OUTPUT="+output,
		"GITHUB_SERVER_URL=https://github.example", "GITHUB_REPOSITORY=o/r", "GITHUB_RUN_ID=42",
		"ISSUE_TITLE="+issueTitle,
	)
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	r := watchRun{out: string(out)}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		r.exit = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(runner, "finding.md"))
	r.body = string(b)
	o, _ := os.ReadFile(output)
	r.ghOut = string(o)
	c, _ := os.ReadFile(filepath.Join(stub, "calls"))
	r.ghCall = string(c)
	return r
}

const compareStep = "Compare the pin with the latest release and published advisories"

func TestReleaseWatchCompare(t *testing.T) {
	pinnedAt := `{"published_at":"2026-09-09T15:27:11Z","tag_name":"v2.197.0"}`
	older := `{"ghsa_id":"GHSA-old0-0000-0000","published_at":"2026-03-11T08:25:57Z"}`
	same := `{"ghsa_id":"GHSA-same-0000-0000","published_at":"2026-09-09T15:27:11Z"}`
	newer := `{"ghsa_id":"GHSA-new0-0000-0000","published_at":"2026-09-20T00:00:00Z"}`
	page2 := `{"ghsa_id":"GHSA-pag2-0000-0000","published_at":"2026-09-21T00:00:00Z"}`
	current := `{"tag_name":"v2.197.0"}`

	for _, c := range []struct {
		name       string
		dockerfile string
		latest     string
		advisories string
		wantExit   int
		wantIn     []string
		wantNotIn  []string
	}{
		{"current pin, only older advisories", plannedBody, current, "[" + older + "," + same + "]\n",
			0, []string{"No finding: v2.197.0"}, nil},
		{"newer release, no new advisory", plannedBody, `{"tag_name":"v2.198.0"}`, "[" + older + "]\n",
			1, []string{"`v2.197.0`", "`v2.198.0`", "after the pin: none."}, nil},
		{"advisory after the pin, no release", plannedBody, current, "[" + older + "," + same + "," + newer + "]\n",
			1, []string{"GHSA-new0-0000-0000", "Latest release: `v2.197.0`"}, []string{"GHSA-old0", "GHSA-same"}},
		{"advisory on the second page", plannedBody, current, "[" + older + "]\n[" + page2 + "]\n",
			1, []string{"GHSA-pag2-0000-0000"}, []string{"GHSA-old0"}},
		{"newer release and advisory together", plannedBody, `{"tag_name":"v2.198.0"}`, "[" + newer + "]\n",
			1, []string{"`v2.198.0`", "GHSA-new0-0000-0000"}, nil},
		{"unreadable pin", "FROM ghcr.io/supabase/auth:v2.197.0\n", current, "[]\n",
			2, nil, nil},
		{"empty latest tag", plannedBody, `{"tag_name":""}`, "[]\n",
			2, nil, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := runWatchStep(t, compareStep, c.dockerfile, map[string]string{
				"tag.json": pinnedAt, "latest.json": c.latest, "advisories.json": c.advisories,
			})
			if r.exit != c.wantExit {
				t.Fatalf("exit = %d, want %d\n%s", r.exit, c.wantExit, r.out)
			}
			finding := strings.Contains(r.ghOut, "finding=true")
			if finding != (c.wantExit == 1) {
				t.Errorf("finding output = %v, want %v (GITHUB_OUTPUT %q)", finding, c.wantExit == 1, r.ghOut)
			}
			if c.wantExit == 1 && !strings.HasPrefix(r.body, "Kind: finding.") {
				t.Fatalf("finding body does not open with its kind\nbody:\n%s\nlog:\n%s", r.body, r.out)
			}
			for _, s := range c.wantIn {
				if !strings.Contains(r.body+r.out, s) {
					t.Errorf("output lacks %q\nbody:\n%s\nlog:\n%s", s, r.body, r.out)
				}
			}
			for _, s := range c.wantNotIn {
				if strings.Contains(r.body, s) {
					t.Errorf("body names %q, which is not after the pin\n%s", s, r.body)
				}
			}
		})
	}
}

const issueStep = "Open or update the patch-due issue"

func TestReleaseWatchIssueFoundByExactTitle(t *testing.T) {
	for _, c := range []struct {
		name    string
		issues  string
		want    []string
		notWant []string
	}{
		{"no issue yet", `[{"number":9,"title":"supabase/auth patch due (v2)","state":"OPEN"}]`,
			[]string{"issue create --title " + issueTitle + " --body-file "},
			[]string{"issue edit", "issue reopen"}},
		{"open issue, a near-title and an older closed one",
			`[{"number":3,"title":"` + issueTitle + `","state":"CLOSED"},{"number":7,"title":"` + issueTitle + `","state":"OPEN"},{"number":9,"title":"supabase/auth patch due (v2)","state":"OPEN"}]`,
			[]string{"issue edit 7 --body-file "},
			[]string{"issue create", "issue reopen", "issue edit 9", "issue edit 3"}},
		{"closed issue only", `[{"number":5,"title":"` + issueTitle + `","state":"CLOSED"}]`,
			[]string{"issue reopen 5", "issue edit 5 --body-file "},
			[]string{"issue create"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := runWatchStep(t, issueStep, plannedBody, map[string]string{
				"issues.json": c.issues, "finding.md": "Pinned: `v2.197.0`\n",
			}, "FINDING=true")
			if r.exit != 0 {
				t.Fatalf("exit = %d, want 0\n%s", r.exit, r.out)
			}
			if !strings.Contains(r.ghCall, "issue list") {
				t.Fatalf("no issue list call; calls:\n%s", r.ghCall)
			}
			for _, s := range c.want {
				if !strings.Contains(r.ghCall, s) {
					t.Errorf("calls lack %q; calls:\n%s", s, r.ghCall)
				}
			}
			for _, s := range c.notWant {
				if strings.Contains(r.ghCall, s) {
					t.Errorf("calls include %q; calls:\n%s", s, r.ghCall)
				}
			}
		})
	}
}

// A compare error opens the issue too, and the body says it is an error, not a finding.
func TestReleaseWatchIssueBodyNamesTheFailureKind(t *testing.T) {
	for _, c := range []struct {
		name, finding string
		fixtures      map[string]string
		want          []string
		notWant       string
	}{
		{"finding", "true", map[string]string{"issues.json": "[]", "finding.md": "Kind: finding.\nPinned: `v2.197.0`\n"},
			[]string{"Kind: finding.", "Pinned: `v2.197.0`"}, "Kind: error."},
		{"error", "", map[string]string{"issues.json": "[]"},
			[]string{"Kind: error.", "https://github.example/o/r/actions/runs/42"}, "Kind: finding."},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := runWatchStep(t, issueStep, plannedBody, c.fixtures, "FINDING="+c.finding)
			if r.exit != 0 {
				t.Fatalf("exit = %d, want 0\n%s", r.exit, r.out)
			}
			if !strings.Contains(r.ghCall, "issue create --title "+issueTitle+" --body-file ") {
				t.Fatalf("no issue create; calls:\n%s", r.ghCall)
			}
			for _, s := range c.want {
				if !strings.Contains(r.body, s) {
					t.Errorf("issue body lacks %q\n%s", s, r.body)
				}
			}
			if strings.Contains(r.body, c.notWant) {
				t.Errorf("issue body says %q\n%s", c.notWant, r.body)
			}
		})
	}
}
