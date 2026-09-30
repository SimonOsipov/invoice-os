// await_ci_test.go runs scripts/ci/await-ci.sh against a scripted gh:
// the shared CI poll of dev-env.yml's await-ci and ci-watch jobs.
package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const ciSHA = "5e975e718251c892c7cbfd3602bf6aa009f37ce5"

var ciNonSuccess = []string{"failure", "cancelled", "timed_out", "action_required", "neutral", "skipped", "stale"}

// ciShim is a gh on PATH that answers one line of gh.seq per call, then gh.default. The line
// EXIT1, or neither file, is a failed call with no stdout. argv.log holds the tab-joined argv.
type ciShim struct {
	dir, out string
}

func newCIShim(t *testing.T) ciShim {
	t.Helper()
	dir := t.TempDir()
	stub := `#!/bin/sh
dir='` + dir + `'
sep=""
for a in "$@"; do printf '%s%s' "$sep" "$a"; sep="$(printf '\t')"; done >> "$dir/argv.log"
printf '\n' >> "$dir/argv.log"
if [ -s "$dir/gh.seq" ]; then
  line=$(head -n 1 "$dir/gh.seq"); tail -n +2 "$dir/gh.seq" > "$dir/gh.seq.tmp"; mv "$dir/gh.seq.tmp" "$dir/gh.seq"
elif [ -f "$dir/gh.default" ]; then line=$(cat "$dir/gh.default")
else line=EXIT1; fi
if [ "$line" = EXIT1 ]; then echo "gh: HTTP 502" >&2; exit 1; fi
printf '%s\n' "$line"
`
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSleepStub(t, dir)
	return ciShim{dir: dir, out: filepath.Join(t.TempDir(), "out")}
}

// ciRun is one CI check-run; an empty conclusion is JSON null.
func ciRun(status, conclusion, startedAt string) string {
	c := "null"
	if conclusion != "" {
		c = `"` + conclusion + `"`
	}
	return `{"name":"CI","status":"` + status + `","conclusion":` + c + `,"started_at":"` + startedAt + `"}`
}

func ciResp(runs ...string) string { return `{"check_runs":[` + strings.Join(runs, ",") + `]}` }

func (s ciShim) seq(t *testing.T, lines ...string) {
	t.Helper()
	writeFile(t, filepath.Join(s.dir, "gh.seq"), strings.Join(lines, "\n")+"\n")
}

func (s ciShim) always(t *testing.T, line string) {
	t.Helper()
	writeFile(t, filepath.Join(s.dir, "gh.default"), line+"\n")
}

func (s ciShim) defaultEnv() string {
	return "export REPO=o/r GH_TOKEN=fake GITHUB_OUTPUT='" + s.out + "'\n"
}

// ciScript fails the test when the script is missing or not executable.
func ciScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(repoRoot(t), "scripts", "ci", "await-ci.sh")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("scripts/ci/await-ci.sh does not exist: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("scripts/ci/await-ci.sh is not executable (mode %v)", info.Mode())
	}
	return path
}

// run puts env (the prelude that sets or unsets REPO, GH_TOKEN, GITHUB_OUTPUT) before the script.
func (s ciShim) run(t *testing.T, env string, args ...string) (output string, code int) {
	t.Helper()
	script := ciScript(t)
	stdout, stderr, code := runBashScript(t, "export PATH='"+s.dir+"':\"$PATH\"\n"+env+"bash '"+script+"' \"$@\"\n", args...)
	return stdout + stderr, code
}

func (s ciShim) argv(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.dir, "argv.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

func (s ciShim) sleeps(t *testing.T) []string {
	t.Helper()
	return railwayShim{dir: s.dir}.sleeps(t)
}

// outputLines is what the script appended to $GITHUB_OUTPUT; an absent file is empty.
func (s ciShim) outputLines(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(s.out)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(raw))
}

func (s ciShim) wantOutput(t *testing.T, want ...string) {
	t.Helper()
	if got := s.outputLines(t); !reflect.DeepEqual(got, want) {
		t.Errorf("GITHUB_OUTPUT = %q, want %q", got, want)
	}
}

func (s ciShim) wantCalls(t *testing.T, want int) {
	t.Helper()
	if n := len(s.argv(t)); n != want {
		t.Errorf("gh calls = %d, want %d", n, want)
	}
}

func TestAwaitCI_SuccessExitsZero(t *testing.T) {
	s := newCIShim(t)
	s.seq(t, ciResp(ciRun("completed", "success", "2026-09-30T10:00:00Z")))
	out, code := s.run(t, s.defaultEnv(), ciSHA)

	if code != 0 {
		t.Errorf("exit %d, want 0; output = %q", code, out)
	}
	if !strings.Contains(out, "CI is green") {
		t.Errorf("output lacks %q: %q", "CI is green", out)
	}
	s.wantOutput(t, "conclusion=success")
	s.wantCalls(t, 1)
	if got := s.sleeps(t); len(got) != 0 {
		t.Errorf("sleeps = %v, want none on an immediate verdict", got)
	}
}

func TestAwaitCI_FailsOnEveryNonSuccessConclusion(t *testing.T) {
	for _, c := range ciNonSuccess {
		t.Run(c, func(t *testing.T) {
			s := newCIShim(t)
			s.seq(t, ciResp(ciRun("completed", c, "2026-09-30T10:00:00Z")))
			out, code := s.run(t, s.defaultEnv(), ciSHA)

			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, out)
			}
			if want := "CI concluded '" + c + "'"; !strings.Contains(out, want) {
				t.Errorf("output lacks %q: %q", want, out)
			}
			if strings.Contains(out, "CI is green") {
				t.Errorf("a %s CI printed green: %q", c, out)
			}
			s.wantOutput(t, "conclusion="+c)
			s.wantCalls(t, 1)
		})
	}
}

func TestAwaitCI_PollsUntilCompleted(t *testing.T) {
	s := newCIShim(t)
	s.seq(t,
		ciResp(ciRun("in_progress", "", "2026-09-30T10:00:00Z")),
		ciResp(ciRun("queued", "", "2026-09-30T10:00:00Z")),
		ciResp(ciRun("completed", "success", "2026-09-30T10:00:00Z")))
	out, code := s.run(t, s.defaultEnv(), ciSHA)

	if code != 0 {
		t.Errorf("exit %d, want 0; output = %q", code, out)
	}
	s.wantCalls(t, 3)
	sleeps := s.sleeps(t)
	if len(sleeps) != 2 {
		t.Errorf("sleeps = %v, want 2 (one after each pending answer)", sleeps)
	}
	for _, v := range sleeps {
		if v != "15" {
			t.Errorf("sleeps = %v, want every sleep to be 15", sleeps)
			break
		}
	}
	s.wantOutput(t, "conclusion=success")
}

func TestAwaitCI_NoCheckRunYetKeepsPolling(t *testing.T) {
	s := newCIShim(t)
	s.seq(t, `{"check_runs":[]}`, ciResp(ciRun("completed", "success", "2026-09-30T10:00:00Z")))
	out, code := s.run(t, s.defaultEnv(), ciSHA)

	if code != 0 {
		t.Errorf("exit %d, want 0; output = %q", code, out)
	}
	s.wantCalls(t, 2)
	if n := len(s.sleeps(t)); n != 1 {
		t.Errorf("sleeps = %d, want 1", n)
	}
}

func TestAwaitCI_APIErrorKeepsPolling(t *testing.T) {
	s := newCIShim(t)
	s.seq(t, "EXIT1", ciResp(ciRun("completed", "success", "2026-09-30T10:00:00Z")))
	out, code := s.run(t, s.defaultEnv(), ciSHA)

	if code != 0 {
		t.Errorf("exit %d, want 0; output = %q", code, out)
	}
	s.wantCalls(t, 2)
	if n := len(s.sleeps(t)); n != 1 {
		t.Errorf("sleeps = %d, want 1", n)
	}
}

func TestAwaitCI_CompletedWithoutConclusionKeepsPolling(t *testing.T) {
	s := newCIShim(t)
	s.seq(t,
		ciResp(ciRun("completed", "", "2026-09-30T10:00:00Z")),
		ciResp(ciRun("completed", "failure", "2026-09-30T10:00:00Z")))
	out, code := s.run(t, s.defaultEnv(), ciSHA)

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, out)
	}
	if !strings.Contains(out, "CI concluded 'failure'") {
		t.Errorf("output lacks the failure verdict: %q", out)
	}
	s.wantCalls(t, 2)
	s.wantOutput(t, "conclusion=failure")
}

// The array order differs from the start order, so neither first nor last in the array wins.
func TestAwaitCI_ReadsTheLatestCheckRun(t *testing.T) {
	const early, late = "2026-09-30T10:00:00Z", "2026-09-30T10:05:00Z"
	for _, c := range []struct {
		name     string
		runs     []string
		wantCode int
		want     string
	}{
		{"success is later, failure listed last", []string{ciRun("completed", "success", late), ciRun("completed", "failure", early)}, 0, "success"},
		{"success is later, failure listed first", []string{ciRun("completed", "failure", early), ciRun("completed", "success", late)}, 0, "success"},
		{"failure is later, failure listed last", []string{ciRun("completed", "success", early), ciRun("completed", "failure", late)}, 1, "failure"},
		{"failure is later, failure listed first", []string{ciRun("completed", "failure", late), ciRun("completed", "success", early)}, 1, "failure"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newCIShim(t)
			s.seq(t, ciResp(c.runs...))
			out, code := s.run(t, s.defaultEnv(), ciSHA)

			if code != c.wantCode {
				t.Errorf("exit %d, want %d; output = %q", code, c.wantCode, out)
			}
			s.wantOutput(t, "conclusion="+c.want)
			s.wantCalls(t, 1)
		})
	}
}

func TestAwaitCI_TimesOutAfterBudget(t *testing.T) {
	s := newCIShim(t)
	s.always(t, ciResp(ciRun("in_progress", "", "2026-09-30T10:00:00Z")))
	out, code := s.run(t, s.defaultEnv(), ciSHA)

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, out)
	}
	if !strings.Contains(out, "::error::Timed out") {
		t.Errorf("output lacks %q: %q", "::error::Timed out", out)
	}
	s.wantCalls(t, 80)
	sleeps := s.sleeps(t)
	if len(sleeps) == 0 {
		t.Error("the script never slept between polls")
	}
	for _, v := range sleeps {
		if v != "15" {
			t.Errorf("sleeps = %v, want every sleep to be 15", sleeps)
			break
		}
	}
	s.wantOutput(t)
}

func TestAwaitCI_QueriesTheCICheckOnTheGivenSHA(t *testing.T) {
	s := newCIShim(t)
	s.seq(t, ciResp(ciRun("completed", "success", "2026-09-30T10:00:00Z")))
	out, code := s.run(t, s.defaultEnv(), ciSHA)

	if code != 0 {
		t.Errorf("exit %d, want 0; output = %q", code, out)
	}
	calls := s.argv(t)
	if len(calls) == 0 {
		t.Fatal("the gh stub logged no call")
	}
	if want := "api\trepos/o/r/commits/" + ciSHA + "/check-runs?check_name=CI&per_page=100"; calls[0] != want {
		t.Errorf("gh argv = %q, want %q", calls[0], want)
	}
}

func TestAwaitCI_UsageErrors(t *testing.T) {
	for _, c := range []struct {
		name string
		env  func(s ciShim) string
		args []string
	}{
		{"no argument", func(s ciShim) string { return s.defaultEnv() }, nil},
		{"empty sha", func(s ciShim) string { return s.defaultEnv() }, []string{""}},
		{"REPO unset", func(s ciShim) string { return "unset REPO\nexport GH_TOKEN=fake GITHUB_OUTPUT='" + s.out + "'\n" }, []string{ciSHA}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newCIShim(t)
			s.seq(t, ciResp(ciRun("completed", "success", "2026-09-30T10:00:00Z")))
			out, code := s.run(t, c.env(s), c.args...)

			if code != 2 {
				t.Errorf("exit %d, want 2; output = %q", code, out)
			}
			if !strings.Contains(out, "::error::") {
				t.Errorf("output lacks ::error::: %q", out)
			}
			if calls := s.argv(t); len(calls) != 0 {
				t.Errorf("gh calls = %q, want none", calls)
			}
			s.wantOutput(t)
		})
	}
}

func TestAwaitCI_NoGithubOutputStillDecides(t *testing.T) {
	env := func(s ciShim) string { return "unset GITHUB_OUTPUT\nexport REPO=o/r GH_TOKEN=fake\n" }

	s := newCIShim(t)
	s.seq(t, ciResp(ciRun("completed", "success", "2026-09-30T10:00:00Z")))
	out, code := s.run(t, env(s), ciSHA)
	if code != 0 {
		t.Errorf("success: exit %d, want 0; output = %q", code, out)
	}
	if !strings.Contains(out, "CI is green") {
		t.Errorf("success: output lacks %q: %q", "CI is green", out)
	}

	s = newCIShim(t)
	s.seq(t, ciResp(ciRun("completed", "failure", "2026-09-30T10:00:00Z")))
	out, code = s.run(t, env(s), ciSHA)
	if code != 1 {
		t.Errorf("failure: exit %d, want 1; output = %q", code, out)
	}
	if !strings.Contains(out, "CI concluded 'failure'") {
		t.Errorf("failure: output lacks the verdict: %q", out)
	}
}
