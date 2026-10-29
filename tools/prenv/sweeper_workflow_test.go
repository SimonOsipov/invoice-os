package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runSweep runs the Sweep step of dev-env-sweeper.yml against a stub preview.sh, a gh shim and a date shim.
// states maps a PR to what `gh pr view` prints.
func runSweep(t *testing.T, states map[string]string) (summary, calls string) {
	t.Helper()
	var run string
	for _, j := range workflowJobsOf(readWorkflow(t, "dev-env-sweeper.yml")) {
		for _, s := range j.steps() {
			if s.keys["name"] == "Sweep" {
				run = s.keys["run"]
			}
		}
	}
	if run == "" {
		t.Fatal("no Sweep step in dev-env-sweeper.yml")
	}
	dir, shims := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "scripts/ci"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeIn(t, filepath.Join(dir, "scripts/ci"), "preview.sh", `case "$1" in
prs) printf 'nightly old\n7 old\n' ;;
*) echo "preview $*" >> '`+dir+`/calls' ;;
esac
`)
	var gh strings.Builder
	gh.WriteString("echo \"gh $*\" >> '" + dir + "/calls'\ncase \"$3\" in\n")
	for pr, st := range states {
		gh.WriteString(pr + ") echo " + st + " ;;\n")
	}
	gh.WriteString("*) exit 1 ;;\nesac\n")
	writeShim(t, filepath.Join(shims, "gh"), gh.String())
	writeShim(t, filepath.Join(shims, "date"), `case "$*" in
*"24 hours ago"*) echo 1000 ;;
*"-d old"*) echo 10 ;;
*) echo now ;;
esac
`)
	writeIn(t, dir, "run.sh", run)
	cmd := exec.Command("bash", "run.sh")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+shims+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GITHUB_STEP_SUMMARY="+filepath.Join(dir, "summary"), "GITHUB_REPOSITORY=o/r", "GH_TOKEN=x")
	if _, stderr, err := runToFiles(t, cmd, false); err != nil {
		t.Fatalf("sweep failed: %v\n%s", err, stderr)
	}
	read := func(n string) string {
		b, _ := os.ReadFile(filepath.Join(dir, n))
		return string(b)
	}
	return read("summary"), read("calls")
}

func TestSweeperKeepsTheNightlyByName(t *testing.T) {
	t.Parallel()
	summary, calls := runSweep(t, map[string]string{"7": "CLOSED"})
	if !strings.Contains(summary, "| #nightly | old | kept: the Nightly run deletes it |") {
		t.Errorf("no kept row for the nightly:\n%s", summary)
	}
	if strings.Contains(calls, "gh pr view nightly") || strings.Contains(calls, "teardown nightly") {
		t.Errorf("the nightly was looked up or torn down:\n%s", calls)
	}
	if !strings.Contains(calls, "preview teardown 7") {
		t.Errorf("the closed PR 7 was not torn down:\n%s", calls)
	}
}

func TestSweeperStillKeepsAnOpenPR(t *testing.T) {
	t.Parallel()
	summary, calls := runSweep(t, map[string]string{"7": "OPEN"})
	if !strings.Contains(summary, "| #7 | old | kept: PR open |") || strings.Contains(calls, "teardown") {
		t.Errorf("open PR not kept:\n%s\n%s", summary, calls)
	}
}
