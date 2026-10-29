// nightly_ci_script_test.go runs scripts/ci/nightly-ci.sh against a scripted gh.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nightlyShim is a gh on PATH keyed by its first two arguments (workflow_run, run_list, run_view,
// run_cancel). Each key answers one line of <key>.seq per call, then <key>.default, else nothing.
// The line EXIT1:<text> fails with <text> on stderr. argv.log holds the tab-joined argv.
type nightlyShim struct {
	dir, out string
}

func newNightlyShim(t *testing.T) nightlyShim {
	t.Helper()
	dir := t.TempDir()
	stub := `#!/bin/sh
dir='` + dir + `'
sep=""
for a in "$@"; do printf '%s%s' "$sep" "$a"; sep="$(printf '\t')"; done >> "$dir/argv.log"
printf '\n' >> "$dir/argv.log"
key="$1_$2"
line=""
if [ -s "$dir/$key.seq" ]; then
  line=$(head -n 1 "$dir/$key.seq"); tail -n +2 "$dir/$key.seq" > "$dir/$key.tmp"; mv "$dir/$key.tmp" "$dir/$key.seq"
elif [ -f "$dir/$key.default" ]; then line=$(cat "$dir/$key.default"); fi
case "$line" in EXIT1:*) echo "${line#EXIT1:}" >&2; exit 1;; esac
printf '%s\n' "$line"
`
	writeShim(t, filepath.Join(dir, "gh"), stub)
	writeSleepStub(t, dir)
	return nightlyShim{dir: dir, out: filepath.Join(t.TempDir(), "out")}
}

func (s nightlyShim) answer(t *testing.T, key string, lines ...string) {
	t.Helper()
	writeFile(t, filepath.Join(s.dir, key+".seq"), strings.Join(lines, "\n")+"\n")
}

func (s nightlyShim) always(t *testing.T, key, line string) {
	t.Helper()
	writeFile(t, filepath.Join(s.dir, key+".default"), line+"\n")
}

func (s nightlyShim) run(t *testing.T, env string, args ...string) (output string, code int) {
	t.Helper()
	script := filepath.Join(repoRoot(t), "scripts", "ci", "nightly-ci.sh")
	info, err := os.Stat(script)
	if err != nil {
		t.Fatalf("scripts/ci/nightly-ci.sh does not exist: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("scripts/ci/nightly-ci.sh is not executable (mode %v)", info.Mode())
	}
	stdout, stderr, code := runBashScript(t, "export PATH='"+s.dir+"':\"$PATH\"\n"+env+"bash '"+script+"' \"$@\"\n", args...)
	return stdout + stderr, code
}

func (s nightlyShim) defaultEnv() string {
	return "export REPO=o/r GH_TOKEN=fake GITHUB_OUTPUT='" + s.out + "'\n"
}

// calls counts the gh calls whose argv starts with the tab-joined prefix.
func (s nightlyShim) calls(t *testing.T, prefix string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.dir, "argv.log"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

func (s nightlyShim) outputText(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(s.out)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestNightlyCIScript_RunIDFromURL(t *testing.T) {
	t.Parallel()
	s := newNightlyShim(t)
	s.always(t, "workflow_run", "https://github.com/o/r/actions/runs/42")
	s.answer(t, "run_view", "in_progress", "completed")
	out, code := s.run(t, s.defaultEnv(), "epic/nightly")
	if code != 0 {
		t.Fatalf("exit %d, want 0\n%s", code, out)
	}
	if got := s.outputText(t); got != "run_id=42\n" {
		t.Errorf("GITHUB_OUTPUT = %q, want run_id=42", got)
	}
	if n := s.calls(t, "run\tlist"); n != 0 {
		t.Errorf("gh run list called %d times, want 0", n)
	}
	if n := s.calls(t, "workflow\trun\tci.yml\t-R\to/r\t--ref\tepic/nightly"); n != 1 {
		t.Errorf("gh workflow run with ci.yml, --ref and the ref called %d times, want 1", n)
	}
	if n := s.calls(t, "run\tview\t42"); n != 2 {
		t.Errorf("gh run view 42 called %d times, want 2", n)
	}
}

func TestNightlyCIScript_RunIDFromListFallback(t *testing.T) {
	t.Parallel()
	s := newNightlyShim(t)
	s.always(t, "run_list", `[{"databaseId":7,"createdAt":"2000-01-01T00:00:00Z"},{"databaseId":9,"createdAt":"2999-01-01T00:00:00Z"}]`)
	s.always(t, "run_view", "completed")
	out, code := s.run(t, s.defaultEnv(), "main")
	if code != 0 {
		t.Fatalf("exit %d, want 0\n%s", code, out)
	}
	if got := s.outputText(t); got != "run_id=9\n" {
		t.Errorf("GITHUB_OUTPUT = %q, want run_id=9 (run 7 predates the dispatch)", got)
	}
	if n := s.calls(t, "run\tlist\t-R\to/r\t--workflow\tci.yml\t--event\tworkflow_dispatch\t--branch\tmain"); n != 1 {
		t.Errorf("gh run list for the dispatch called %d times, want 1", n)
	}
}

func TestNightlyCIScript_NoRunFoundExitsOne(t *testing.T) {
	t.Parallel()
	s := newNightlyShim(t)
	s.always(t, "run_list", "[]")
	out, code := s.run(t, s.defaultEnv(), "main")
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s", code, out)
	}
	if n := s.calls(t, "run\tlist"); n != 12 {
		t.Errorf("gh run list called %d times, want 12", n)
	}
	if got := s.outputText(t); got != "" {
		t.Errorf("GITHUB_OUTPUT = %q, want empty", got)
	}
}

func TestNightlyCIScript_RedCIStillExitsZero(t *testing.T) {
	t.Parallel()
	s := newNightlyShim(t)
	s.always(t, "workflow_run", "https://github.com/o/r/actions/runs/42")
	// run view asks only for .status; the conclusion of the run is the summary's business.
	s.always(t, "run_view", "completed")
	if out, code := s.run(t, s.defaultEnv(), "main"); code != 0 {
		t.Fatalf("exit %d, want 0\n%s", code, out)
	}
}

func TestNightlyCIScript_GhViewErrorIsNotYet(t *testing.T) {
	t.Parallel()
	s := newNightlyShim(t)
	s.always(t, "workflow_run", "https://github.com/o/r/actions/runs/42")
	s.answer(t, "run_view", "EXIT1:gh: HTTP 502", "EXIT1:gh: HTTP 502", "completed")
	out, code := s.run(t, s.defaultEnv(), "main")
	if code != 0 {
		t.Fatalf("exit %d, want 0\n%s", code, out)
	}
	if n := s.calls(t, "run\tview\t42"); n != 3 {
		t.Errorf("gh run view called %d times, want 3", n)
	}
}

func TestNightlyCIScript_TimeoutCancelsAndExitsOne(t *testing.T) {
	t.Parallel()
	s := newNightlyShim(t)
	s.always(t, "workflow_run", "https://github.com/o/r/actions/runs/42")
	s.always(t, "run_view", "in_progress")
	out, code := s.run(t, s.defaultEnv()+"export NIGHTLY_CI_POLLS=3\n", "main")
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s", code, out)
	}
	if n := s.calls(t, "run\tview\t42"); n != 3 {
		t.Errorf("gh run view called %d times, want 3", n)
	}
	if n := s.calls(t, "run\tcancel\t42\t-R\to/r"); n != 1 {
		t.Errorf("gh run cancel 42 called %d times, want 1", n)
	}
}

func TestNightlyCIScript_DispatchFailureExitsOne(t *testing.T) {
	t.Parallel()
	s := newNightlyShim(t)
	s.always(t, "workflow_run", "EXIT1:HTTP 404")
	out, code := s.run(t, s.defaultEnv(), "main")
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s", code, out)
	}
	if !strings.Contains(out, "HTTP 404") {
		t.Errorf("output lacks the HTTP 404 text: %q", out)
	}
	if n := s.calls(t, "run\t"); n != 0 {
		t.Errorf("gh run called %d times after a failed dispatch, want 0", n)
	}
}

func TestNightlyCIScript_UsageErrors(t *testing.T) {
	t.Parallel()
	s := newNightlyShim(t)
	if out, code := s.run(t, s.defaultEnv()); code != 2 {
		t.Errorf("no argument: exit %d, want 2\n%s", code, out)
	}
	if out, code := s.run(t, "unset REPO\n", "main"); code != 2 {
		t.Errorf("REPO unset: exit %d, want 2\n%s", code, out)
	}
	if n := s.calls(t, ""); n > 0 {
		t.Errorf("gh called %d times on a usage error, want 0", n)
	}
}
