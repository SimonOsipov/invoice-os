package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runSweep runs the Sweep step of dev-env-sweeper.yml against a stub preview.sh, a gh shim and a date shim.
// sweepCase: prs and dbs are what the stub preview.sh prints for `prs` and `databases`; prsAgain answers later `prs` calls.
// states maps a PR to what `gh pr view` prints.
type sweepCase struct {
	prs, prsAgain, dbs string
	failDatabases      bool
	failRecheck        bool // a `prs` call after the first fails
	failTeardown       []string
	states             map[string]string
}

func runSweep(t *testing.T, sw sweepCase) (summary, calls string, code int) {
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
	writeIn(t, dir, "prs", sw.prs)
	writeIn(t, dir, "dbs", sw.dbs)
	if sw.prsAgain != "" {
		writeIn(t, dir, "prs-again", sw.prsAgain)
	}
	if sw.failDatabases {
		writeIn(t, dir, "fail-databases", "")
	}
	if sw.failRecheck {
		writeIn(t, dir, "fail-recheck", "")
	}
	writeIn(t, dir, "fail-teardown", strings.Join(sw.failTeardown, "\n")+"\n")
	writeIn(t, filepath.Join(dir, "scripts/ci"), "preview.sh", `d='`+dir+`'
case "$1" in
prs) if [ -e "$d/prs-seen" ] && [ -e "$d/fail-recheck" ]; then exit 1; fi
  if [ -e "$d/prs-seen" ] && [ -e "$d/prs-again" ]; then cat "$d/prs-again"; else touch "$d/prs-seen"; cat "$d/prs"; fi ;;
databases) [ ! -e "$d/fail-databases" ] || exit 1; cat "$d/dbs" ;;
teardown) echo "preview $*" >> "$d/calls"; ! grep -qxF -- "$2" "$d/fail-teardown" ;;
*) echo "preview $*" >> "$d/calls" ;;
esac
`)
	var gh strings.Builder
	gh.WriteString("echo \"gh $*\" >> '" + dir + "/calls'\ncase \"$3\" in\n")
	for pr, st := range sw.states {
		gh.WriteString(pr + ") echo " + st + " ;;\n")
	}
	gh.WriteString("*) exit 1 ;;\nesac\n")
	writeShim(t, filepath.Join(shims, "gh"), gh.String())
	writeShim(t, filepath.Join(shims, "date"), `case "$*" in
*"24 hours ago"*) echo 1000 ;;
*"-d old"*) echo 10 ;;
*"-d young"*) echo 99999 ;;
*"-d -"*) echo 99999 ;;
*) echo now ;;
esac
`)
	writeIn(t, dir, "run.sh", run)
	cmd := exec.Command("bash", "run.sh")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+shims+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GITHUB_STEP_SUMMARY="+filepath.Join(dir, "summary"), "GITHUB_REPOSITORY=o/r", "GH_TOKEN=x")
	stdout, stderr, err := runToFiles(t, cmd, false)
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("sweep did not run: %v", err)
		}
		code = ee.ExitCode()
	}
	read := func(n string) string {
		b, _ := os.ReadFile(filepath.Join(dir, n))
		return string(b)
	}
	return read("summary") + stdout + stderr, read("calls"), code
}

const oldPRs = "nightly old\n7 old\n"

func TestSweeperKeepsTheNightlyByName(t *testing.T) {
	t.Parallel()
	summary, calls, code := runSweep(t, sweepCase{prs: oldPRs, states: map[string]string{"7": "CLOSED"}})
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, summary)
	}
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
	summary, calls, code := runSweep(t, sweepCase{prs: oldPRs, states: map[string]string{"7": "OPEN"}})
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, summary)
	}
	if !strings.Contains(summary, "| #7 | old | kept: PR open |") || strings.Contains(calls, "teardown") {
		t.Errorf("open PR not kept:\n%s\n%s", summary, calls)
	}
}

func TestSweeperDropsADatabaseOnlyMergedPR(t *testing.T) {
	t.Parallel()
	summary, calls, code := runSweep(t, sweepCase{prs: "nightly old\n", dbs: "672\n", states: map[string]string{"672": "MERGED"}})
	if code != 0 || !strings.Contains(calls, "preview teardown 672") || !strings.Contains(summary, "| #672 | - | deleted: PR MERGED |") {
		t.Errorf("exit %d:\n%s\n%s", code, summary, calls)
	}
}

func TestSweeperKeepsADatabaseOnlyOpenPR(t *testing.T) {
	t.Parallel()
	summary, calls, code := runSweep(t, sweepCase{prs: "nightly old\n", dbs: "8\n", states: map[string]string{"8": "OPEN"}})
	if code != 0 || strings.Contains(calls, "teardown") || !strings.Contains(summary, "| #8 | - | kept: PR open |") {
		t.Errorf("exit %d:\n%s\n%s", code, summary, calls)
	}
}

func TestSweeperKeepsTheNightlyDatabase(t *testing.T) {
	t.Parallel()
	summary, calls, code := runSweep(t, sweepCase{prs: "7 old\n", dbs: "nightly\n", states: map[string]string{"7": "OPEN"}})
	if code != 0 || strings.Count(summary, "| #nightly |") != 1 || !strings.Contains(summary, "| #nightly | - | kept: the Nightly run deletes it |") ||
		strings.Contains(calls, "gh pr view nightly") || strings.Contains(calls, "teardown") {
		t.Errorf("exit %d:\n%s\n%s", code, summary, calls)
	}
}

func TestSweeperListsAPRInBothOnce(t *testing.T) {
	t.Parallel()
	summary, calls, code := runSweep(t, sweepCase{prs: "7 old\n", dbs: "7\n", states: map[string]string{"7": "CLOSED"}})
	if code != 0 || strings.Count(summary, "| #7 |") != 1 || strings.Count(calls, "preview teardown 7") != 1 {
		t.Errorf("exit %d:\n%s\n%s", code, summary, calls)
	}
}

func TestSweeperKeepsADatabaseOnlyPRThatCameBack(t *testing.T) {
	t.Parallel()
	summary, calls, code := runSweep(t, sweepCase{prs: "nightly old\n", prsAgain: "nightly old\n672 now\n", dbs: "672\n", states: map[string]string{"672": "CLOSED"}})
	if code != 0 || strings.Contains(calls, "preview teardown 672") || !strings.Contains(summary, "| #672 | - | kept: preview came back |") {
		t.Errorf("exit %d:\n%s\n%s", code, summary, calls)
	}
}

func TestSweeperKeepsADatabaseOnlyPRWhenTheRecheckFails(t *testing.T) {
	t.Parallel()
	summary, calls, code := runSweep(t, sweepCase{prs: "nightly old\n", failRecheck: true, dbs: "672\n", states: map[string]string{"672": "CLOSED"}})
	if strings.Contains(calls, "preview teardown 672") || !strings.Contains(summary, "| #672 | - | kept: recheck failed |") {
		t.Errorf("exit %d:\n%s\n%s", code, summary, calls)
	}
}

func TestSweeperStopsWhenDatabasesCannotBeListed(t *testing.T) {
	t.Parallel()
	summary, calls, code := runSweep(t, sweepCase{prs: oldPRs, failDatabases: true, states: map[string]string{"7": "CLOSED"}})
	if code != 1 || !strings.Contains(summary, "Could not list the preview databases") || strings.Contains(calls, "teardown") {
		t.Errorf("exit %d:\n%s\n%s", code, summary, calls)
	}
}

func TestSweeperFailsOnAFailedDatabaseDrop(t *testing.T) {
	t.Parallel()
	summary, _, code := runSweep(t, sweepCase{prs: "nightly old\n", dbs: "672\n", failTeardown: []string{"672"}, states: map[string]string{"672": "MERGED"}})
	if code != 1 || !strings.Contains(summary, "| #672 | - | DELETE FAILED: PR MERGED |") || !strings.Contains(summary, "1 preview(s) could not be deleted") {
		t.Errorf("exit %d:\n%s", code, summary)
	}
}

// The age guard still holds for a preview that owns resources, whatever its database says.
func TestSweeperKeepsAYoungPRThatAlsoHasADatabase(t *testing.T) {
	t.Parallel()
	summary, calls, code := runSweep(t, sweepCase{prs: "7 young\n", dbs: "7\n", states: map[string]string{"7": "CLOSED"}})
	if code != 0 || strings.Count(summary, "| #7 |") != 1 || !strings.Contains(summary, "| #7 | young | kept: younger than 24 h |") || strings.Contains(calls, "teardown") {
		t.Errorf("exit %d:\n%s\n%s", code, summary, calls)
	}
}

func TestSweeperSkipsADatabaseOnlyPRWhoseStateIsUnreadable(t *testing.T) {
	t.Parallel()
	summary, calls, code := runSweep(t, sweepCase{prs: "nightly old\n", dbs: "9\n"})
	if code != 0 || !strings.Contains(summary, "| #9 | - | skipped: PR state unreadable |") || strings.Contains(calls, "teardown") {
		t.Errorf("exit %d:\n%s\n%s", code, summary, calls)
	}
}
