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
	calls    []string // one line per gh call: its argv joined by spaces
	bodies   string   // content of every --body-file at call time
	comments string   // content of every issue comment --body-file
	code     int
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
	return runAgedEnv(t, list, body, withBodyFile)
}

// runAgedEnv is runAged with extra env entries (NOTIFY=..., AGED_COMMENT_FAIL=1).
func runAgedEnv(t *testing.T, list, body string, withBodyFile bool, env ...string) agedRun {
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
if [ "$1 $2" = "issue comment" ]; then
  [ -n "$AGED_COMMENT_FAIL" ] && exit 1
  while [ $# -gt 0 ]; do
    if [ "$1" = --body-file ]; then cat "$2" >> "$AGED_DIR/comments"; fi
    shift
  done
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
		"GITHUB_SERVER_URL=https://github.example", "GITHUB_REPOSITORY=o/r", "GITHUB_RUN_ID=42",
		"AGED_COMMENT_FAIL=", "NOTIFY=")
	cmd.Env = append(cmd.Env, env...)
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
	comments, _ := os.ReadFile(filepath.Join(work, "comments"))
	return agedRun{calls, string(bodies), string(comments), code}
}

func agedList(items ...string) string { return "[" + strings.Join(items, ",") + "]" }

func agedItem(n, title, state string) string {
	return `{"number":` + n + `,"title":"` + title + `","state":"` + state + `"}`
}

// agedItemBody is an issue list item with a body; rawBody is the JSON value (`null`, `""`, a quoted string).
func agedItemBody(n, title, state, rawBody string) string {
	return `{"number":` + n + `,"title":"` + title + `","state":"` + state + `","body":` + rawBody + `}`
}

func jsonString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}

func agedRow(n, pkg, ghsa, sev, age string) string {
	return "| #" + n + " | " + pkg + " | " + ghsa + " | " + sev + " | " + age + " | https://github.example/a/" + n + " |\n"
}

func agedTable(rows ...string) string {
	return "Intro.\n\n| Alert | Package | GHSA | Severity | Age (days) | Link |\n|---|---|---|---|---|---|\n" + strings.Join(rows, "")
}

var (
	rowOne   = agedRow("1", "pkg-one", "GHSA-aaaa-aaaa-0001", "low", "8")
	rowTwo   = agedRow("2", "pkg-two", "GHSA-aaaa-aaaa-0002", "high", "9")
	rowThree = agedRow("3", "pkg-three", "GHSA-aaaa-aaaa-0003", "medium", "10")
)

func openWith(current string) string {
	return agedList(agedItemBody("7", agedTitle, "OPEN", jsonString(current)))
}

func (r agedRun) index(prefix string) int {
	for i, c := range r.calls {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}
	return -1
}

func countPrefix(r agedRun, prefix string) int {
	n := 0
	for _, c := range r.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func TestAgedAlertsScript_NewAlertOnOpenIssueAddsOneComment(t *testing.T) {
	t.Parallel()
	r := runAged(t, openWith(agedTable(rowOne)), agedTable(rowOne, rowTwo), true)
	if r.code != 0 {
		t.Fatalf("exit %d", r.code)
	}
	if n := countPrefix(r, "issue comment 7 --body-file"); n != 1 {
		t.Fatalf("%d comment calls: %q", n, r.calls)
	}
	if c, e := r.index("issue comment"), r.index("issue edit 7 --body-file"); c < 0 || e < 0 || c > e {
		t.Errorf("comment must come before edit: %q", r.calls)
	}
	for _, want := range []string{"#2", "pkg-two", "GHSA-aaaa-aaaa-0002", "high", "9"} {
		if !strings.Contains(r.comments, want) {
			t.Errorf("comment lacks %q:\n%s", want, r.comments)
		}
	}
	if strings.Contains(r.comments, "#1") {
		t.Errorf("comment names the old alert:\n%s", r.comments)
	}
}

func TestAgedAlertsScript_NewAgesOnlyAddsNoComment(t *testing.T) {
	t.Parallel()
	old := agedTable(agedRow("1", "pkg-one", "GHSA-aaaa-aaaa-0001", "low", "8"), agedRow("2", "pkg-two", "GHSA-aaaa-aaaa-0002", "high", "8"))
	r := runAged(t, openWith(old), agedTable(rowOne, rowTwo), true)
	if r.code != 0 {
		t.Fatalf("exit %d", r.code)
	}
	if r.called("issue comment") {
		t.Errorf("comment called: %q", r.calls)
	}
	if n := countPrefix(r, "issue edit 7"); n != 1 {
		t.Errorf("%d edit calls: %q", n, r.calls)
	}
}

func TestAgedAlertsScript_TwoNewAlertsAddOneComment(t *testing.T) {
	t.Parallel()
	r := runAged(t, openWith(agedTable(rowOne)), agedTable(rowOne, rowTwo, rowThree), true)
	if n := countPrefix(r, "issue comment"); n != 1 {
		t.Fatalf("%d comment calls: %q", n, r.calls)
	}
	for _, want := range []string{"#2", "#3"} {
		if !strings.Contains(r.comments, want) {
			t.Errorf("comment lacks %q:\n%s", want, r.comments)
		}
	}
}

func TestAgedAlertsScript_RemovedAlertAddsNoComment(t *testing.T) {
	t.Parallel()
	r := runAged(t, openWith(agedTable(rowOne, rowTwo)), agedTable(rowTwo), true)
	if r.called("issue comment") || !r.called("issue edit 7") {
		t.Errorf("calls = %q, want edit only", r.calls)
	}
}

func TestAgedAlertsScript_NewAlertWithAnEmptyCurrentBodyIsNew(t *testing.T) {
	t.Parallel()
	row4 := agedRow("4", "pkg-four", "GHSA-aaaa-aaaa-0004", "critical", "11")
	r := runAged(t, agedList(agedItemBody("7", agedTitle, "OPEN", `""`)), agedTable(row4), true)
	if n := countPrefix(r, "issue comment"); n != 1 || !strings.Contains(r.comments, "#4") {
		t.Errorf("calls = %q, comment:\n%s", r.calls, r.comments)
	}
}

func TestAgedAlertsScript_NullOrAbsentCurrentBodyIsEmpty(t *testing.T) {
	t.Parallel()
	row4 := agedRow("4", "pkg-four", "GHSA-aaaa-aaaa-0004", "critical", "11")
	for name, item := range map[string]string{
		"null":   agedItemBody("7", agedTitle, "OPEN", "null"),
		"absent": agedItem("7", agedTitle, "OPEN"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := runAged(t, agedList(item), agedTable(row4), true)
			if r.code != 0 {
				t.Fatalf("exit %d", r.code)
			}
			if n := countPrefix(r, "issue comment"); n != 1 || !strings.Contains(r.comments, "#4") {
				t.Errorf("calls = %q, comment:\n%s", r.calls, r.comments)
			}
		})
	}
}

func TestAgedAlertsScript_CommentMentionsTheOwner(t *testing.T) {
	t.Parallel()
	list, body := openWith(agedTable(rowOne)), agedTable(rowOne, rowTwo)
	r := runAgedEnv(t, list, body, true, "NOTIFY=SimonOsipov")
	if first, _, _ := strings.Cut(r.comments, "\n"); first != "@SimonOsipov" {
		t.Errorf("first line = %q, want @SimonOsipov:\n%s", first, r.comments)
	}
	if r := runAged(t, list, body, true); r.comments == "" || strings.Contains(r.comments, "@") {
		t.Errorf("unset NOTIFY: comment empty or mentions:\n%s", r.comments)
	}
}

func TestAgedAlertsScript_ReopenedIssueAddsNoComment(t *testing.T) {
	t.Parallel()
	list := agedList(agedItemBody("5", agedTitle, "CLOSED", jsonString(agedTable(rowOne))))
	r := runAged(t, list, agedTable(rowOne, rowTwo), true)
	if len(r.calls) != 3 || r.calls[1] != "issue reopen 5" || !strings.HasPrefix(r.calls[2], "issue edit 5") {
		t.Errorf("calls = %q, want list, reopen 5, edit 5", r.calls)
	}
}

func TestAgedAlertsScript_NewIssueAddsNoComment(t *testing.T) {
	t.Parallel()
	r := runAged(t, agedList(), agedTable(rowOne), true)
	if !r.called("issue create") || r.called("issue comment") {
		t.Errorf("calls = %q, want create without comment", r.calls)
	}
}

func TestAgedAlertsScript_CloseAddsNoAlertComment(t *testing.T) {
	t.Parallel()
	r := runAged(t, openWith(agedTable(rowOne)), "", true)
	if len(r.calls) != 2 || !strings.HasPrefix(r.calls[1], "issue close 7 --comment") {
		t.Errorf("calls = %q, want list, close 7 --comment", r.calls)
	}
}

func TestAgedAlertsScript_FailedCommentWritesNoEdit(t *testing.T) {
	t.Parallel()
	r := runAgedEnv(t, openWith(agedTable(rowOne)), agedTable(rowOne, rowTwo), true, "AGED_COMMENT_FAIL=1")
	if r.code == 0 {
		t.Error("exit 0 after a failed comment")
	}
	if r.called("issue edit") || r.bodies != "" {
		t.Errorf("edit after failed comment: %q, bodies %q", r.calls, r.bodies)
	}
}

func TestAgedAlertsScript_AlertNumbersThatArePrefixesAreDistinct(t *testing.T) {
	t.Parallel()
	row12 := agedRow("12", "pkg-twelve", "GHSA-aaaa-aaaa-0012", "low", "9")
	for name, tc := range map[string]struct{ cur, next, want, not string }{
		"twelve known, one new": {agedTable(row12), agedTable(row12, rowOne), "#1 ", "#12"},
		"one known, twelve new": {agedTable(rowOne), agedTable(rowOne, row12), "#12 ", "#1 "},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := runAged(t, openWith(tc.cur), tc.next, true)
			if n := countPrefix(r, "issue comment"); n != 1 {
				t.Fatalf("%d comment calls: %q", n, r.calls)
			}
			if !strings.Contains(r.comments, tc.want) || strings.Contains(r.comments, tc.not) {
				t.Errorf("want %q and not %q:\n%s", tc.want, tc.not, r.comments)
			}
		})
	}
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

func TestAgedAlertsScript_ListSearchesAllStatesByTitle(t *testing.T) {
	t.Parallel()
	r := runAged(t, agedList(), "Alerts: #1\n", true)
	want := `issue list --state all --limit 100 --search in:title "` + agedTitle + `"`
	if !r.called(want) {
		t.Errorf("list call lacks %q: %q", want, r.calls)
	}
}
