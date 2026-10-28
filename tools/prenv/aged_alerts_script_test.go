// aged_alerts_script_test.go runs scripts/ci/aged-alerts-issue.sh with a shim gh and the system jq.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const agedTitle = "Dependabot alerts open more than 7 days"

type agedRun struct {
	calls  []string // one line per gh call: its argv joined by spaces
	bodies string   // content of every --body-file at call time
	code   int
}

func (r agedRun) called(prefix string) bool {
	for _, c := range r.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func (r agedRun) wrote() bool {
	for _, p := range []string{"issue create", "issue edit", "issue reopen", "issue close"} {
		if r.called(p) {
			return true
		}
	}
	return false
}

// runAged runs the script; list is the JSON `gh issue list` prints, or "FAIL" to make it exit 1 silently.
func runAged(t *testing.T, list, body string, withBodyFile bool) agedRun {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not on PATH")
	}
	shims, work := t.TempDir(), t.TempDir()
	writeShim(t, filepath.Join(shims, "gh"), `echo "$*" >> "$AGED_DIR/calls"
if [ "$1 $2" = "issue list" ]; then
  [ "$AGED_LIST" = FAIL ] && exit 1
  printf '%s' "$AGED_LIST"
  exit 0
fi
while [ $# -gt 0 ]; do
  if [ "$1" = --body-file ]; then cat "$2" >> "$AGED_DIR/bodies"; fi
  shift
done
`)
	bodyPath := filepath.Join(work, "aged.md")
	if withBodyFile {
		if err := os.WriteFile(bodyPath, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", filepath.Join(repoRoot(t), "scripts", "ci", "aged-alerts-issue.sh"), bodyPath)
	cmd.Env = append(os.Environ(),
		"PATH="+shims+":"+os.Getenv("PATH"),
		"AGED_DIR="+work, "AGED_LIST="+list,
		"ISSUE_TITLE="+agedTitle,
		"GITHUB_SERVER_URL=https://github.example", "GITHUB_REPOSITORY=o/r", "GITHUB_RUN_ID=42")
	_, _, err := runToFiles(t, cmd, false)
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		code = ee.ExitCode()
	}
	var calls []string
	if b, err := os.ReadFile(filepath.Join(work, "calls")); err == nil {
		calls = strings.Split(strings.TrimSpace(string(b)), "\n")
	}
	bodies, _ := os.ReadFile(filepath.Join(work, "bodies"))
	return agedRun{calls, string(bodies), code}
}

func agedList(items ...string) string { return "[" + strings.Join(items, ",") + "]" }

func agedItem(n, title, state string) string {
	return `{"number":` + n + `,"title":"` + title + `","state":"` + state + `"}`
}

func TestAgedAlertsScript_NoIssueCreates(t *testing.T) {
	t.Parallel()
	r := runAged(t, agedList(agedItem("4", agedTitle+" (v2)", "OPEN")), "Alerts: #1\n", true)
	if r.code != 0 {
		t.Fatalf("exit %d", r.code)
	}
	if !r.called("issue create --title " + agedTitle + " --body-file") {
		t.Errorf("no create call: %q", r.calls)
	}
	if r.called("issue edit") || r.called("issue reopen") {
		t.Errorf("edit or reopen called: %q", r.calls)
	}
}

func TestAgedAlertsScript_OpenIssueIsEdited(t *testing.T) {
	t.Parallel()
	list := agedList(agedItem("3", agedTitle, "CLOSED"), agedItem("7", agedTitle, "OPEN"), agedItem("9", agedTitle+" (v2)", "OPEN"))
	r := runAged(t, list, "Alerts: #1\n", true)
	if r.code != 0 {
		t.Fatalf("exit %d", r.code)
	}
	if !r.called("issue edit 7 --body-file") {
		t.Errorf("no edit 7: %q", r.calls)
	}
	for _, p := range []string{"issue create", "issue reopen", "issue edit 9", "issue edit 3"} {
		if r.called(p) {
			t.Errorf("unexpected %q: %q", p, r.calls)
		}
	}
}

func TestAgedAlertsScript_ClosedIssueIsReopened(t *testing.T) {
	t.Parallel()
	r := runAged(t, agedList(agedItem("5", agedTitle, "CLOSED")), "Alerts: #1\n", true)
	if r.code != 0 {
		t.Fatalf("exit %d", r.code)
	}
	if len(r.calls) != 3 || r.calls[1] != "issue reopen 5" || !strings.HasPrefix(r.calls[2], "issue edit 5 --body-file") {
		t.Errorf("calls = %q, want list, reopen 5, edit 5", r.calls)
	}
	if r.called("issue create") {
		t.Errorf("create called: %q", r.calls)
	}
}

func TestAgedAlertsScript_NearTitleIsNotAMatch(t *testing.T) {
	t.Parallel()
	r := runAged(t, agedList(agedItem("4", agedTitle+" (v2)", "OPEN"), agedItem("6", "x "+agedTitle, "OPEN")), "Alerts: #1\n", true)
	if !r.called("issue create") {
		t.Errorf("no create call: %q", r.calls)
	}
	if r.called("issue edit") {
		t.Errorf("edit called: %q", r.calls)
	}
}

func TestAgedAlertsScript_BodyHasRunURL(t *testing.T) {
	t.Parallel()
	r := runAged(t, agedList(), "Alerts: #23\n", true)
	for _, want := range []string{"Alerts: #23", "https://github.example/o/r/actions/runs/42"} {
		if !strings.Contains(r.bodies, want) {
			t.Errorf("body lacks %q:\n%s", want, r.bodies)
		}
	}
}

func TestAgedAlertsScript_EmptyBodyClosesOpenIssue(t *testing.T) {
	t.Parallel()
	r := runAged(t, agedList(agedItem("7", agedTitle, "OPEN")), "", true)
	if r.code != 0 {
		t.Fatalf("exit %d", r.code)
	}
	if !r.called("issue close 7 --comment") {
		t.Errorf("no close 7 with comment: %q", r.calls)
	}
	if r.called("issue create") || r.called("issue edit") {
		t.Errorf("create or edit called: %q", r.calls)
	}
}

func TestAgedAlertsScript_EmptyBodyClosedOrAbsentWritesNothing(t *testing.T) {
	t.Parallel()
	for name, list := range map[string]string{
		"closed": agedList(agedItem("7", agedTitle, "CLOSED")),
		"absent": agedList(),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := runAged(t, list, "", true)
			if r.code != 0 {
				t.Errorf("exit %d", r.code)
			}
			if r.wrote() {
				t.Errorf("write call: %q", r.calls)
			}
		})
	}
}

func TestAgedAlertsScript_MissingBodyFileFailsClosed(t *testing.T) {
	t.Parallel()
	r := runAged(t, agedList(agedItem("7", agedTitle, "OPEN")), "", false)
	if r.code != 2 {
		t.Errorf("exit %d, want 2", r.code)
	}
	if r.wrote() {
		t.Errorf("write call: %q", r.calls)
	}
}

func TestAgedAlertsScript_ListFailureWritesNothing(t *testing.T) {
	t.Parallel()
	r := runAged(t, "FAIL", "Alerts: #1\n", true)
	if r.code == 0 {
		t.Error("exit 0 after a failed list")
	}
	if r.wrote() {
		t.Errorf("write call: %q", r.calls)
	}
}
