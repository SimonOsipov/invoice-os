// dependabot_only_script_test.go runs scripts/ci/dependabot-only.sh with a shim gh and the system jq.
package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	doBot   = "dependabot[bot]"
	doHuman = "SimonOsipov"
)

var doEnvKeys = []string{"GH_TOKEN", "REPO", "PR_AUTHOR", "EVENT_SENDER", "EVENT_ACTION", "HEAD_SHA", "GITHUB_OUTPUT"}

// doCommit is one entry of the pulls/<n>/commits list. verified is a JSON literal; "" drops the key.
type doCommit struct {
	sha, author, committer, verified string
}

// botCommit is the shape of a real Dependabot commit.
func botCommit() doCommit { return doCommit{author: doBot, committer: "web-flow", verified: "true"} }

func humanCommit() doCommit { return doCommit{author: doHuman, committer: doHuman, verified: "false"} }

func (c doCommit) json() string {
	login := func(l string) string {
		if l == "" {
			return "null"
		}
		b, _ := json.Marshal(map[string]string{"login": l})
		return string(b)
	}
	verification := "{}"
	if c.verified != "" {
		verification = `{"verified":` + c.verified + `}`
	}
	return `{"sha":"` + c.sha + `","author":` + login(c.author) + `,"committer":` + login(c.committer) +
		`,"commit":{"verification":` + verification + `}}`
}

type doOpts struct {
	pages [][]doCommit      // one array per page, printed back to back
	raw   string            // printed instead of pages when set
	then  string            // printed from the second gh call on when set
	fail  bool              // gh exits 1
	env   map[string]string // overrides the defaults
	omit  []string          // env vars left unset
	noArg bool
}

type doRun struct {
	out, stdout, stderr string
	calls               []string
	code                int
}

func (r doRun) msg() string { return "stdout=" + r.stdout + " stderr=" + r.stderr }

// verdict is the value of dependabot_only in the output file, or "" when the file holds none.
func (r doRun) verdict() string {
	return strings.TrimPrefix(strings.TrimSpace(r.out), "dependabot_only=")
}

func runDO(t *testing.T, o doOpts) doRun {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("jq not on PATH in CI")
		}
		t.Skip("jq not on PATH")
	}
	shims, work := t.TempDir(), t.TempDir()
	writeShim(t, filepath.Join(shims, "gh"), `echo "$*" >> "$DO_DIR/calls"
[ -n "${DO_FAIL:-}" ] && exit 1
n=$(wc -l < "$DO_DIR/calls")
if [ "$n" -gt 1 ] && [ -f "$DO_DIR/list2" ]; then cat "$DO_DIR/list2"; else cat "$DO_DIR/list"; fi
`)
	list := o.raw
	if list == "" {
		var sb strings.Builder
		for _, page := range o.pages {
			parts := make([]string, len(page))
			for i, c := range page {
				if c.sha == "" {
					c.sha = "sha" + string(rune('a'+i))
				}
				parts[i] = c.json()
			}
			sb.WriteString("[" + strings.Join(parts, ",") + "]\n")
		}
		list = sb.String()
	}
	if err := os.WriteFile(filepath.Join(work, "list"), []byte(list), 0o644); err != nil {
		t.Fatal(err)
	}
	if o.then != "" {
		if err := os.WriteFile(filepath.Join(work, "list2"), []byte(o.then), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	outPath := filepath.Join(work, "output")
	env := map[string]string{
		"GH_TOKEN": "t", "REPO": "o/r", "PR_AUTHOR": doBot, "EVENT_SENDER": doBot,
		"EVENT_ACTION": "synchronize", "HEAD_SHA": "shahead", "GITHUB_OUTPUT": outPath,
		"DEPENDABOT_ONLY_RETRY_SLEEP": "0",
	}
	for k, v := range o.env {
		env[k] = v
	}
	for _, k := range o.omit {
		delete(env, k)
	}
	var cmdEnv []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		skip := false
		for _, dropped := range doEnvKeys {
			skip = skip || k == dropped
		}
		if !skip {
			cmdEnv = append(cmdEnv, kv)
		}
	}
	cmdEnv = append(cmdEnv, "PATH="+shims+":"+os.Getenv("PATH"), "DO_DIR="+work)
	if o.fail {
		cmdEnv = append(cmdEnv, "DO_FAIL=1")
	}
	for k, v := range env {
		cmdEnv = append(cmdEnv, k+"="+v)
	}
	args := []string{filepath.Join(repoRoot(t), "scripts", "ci", "dependabot-only.sh")}
	if !o.noArg {
		args = append(args, "12")
	}
	cmd := exec.Command("bash", args...)
	cmd.Env = cmdEnv
	stdout, stderr, err := runToFiles(t, cmd, false)
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
	out, _ := os.ReadFile(outPath)
	return doRun{string(out), stdout, stderr, calls, code}
}

// doPage: one page; its last commit gets the default HEAD_SHA.
func doPage(cs ...doCommit) [][]doCommit {
	cs[len(cs)-1].sha = "shahead"
	return [][]doCommit{cs}
}

func wantVerdict(t *testing.T, r doRun, want string) {
	t.Helper()
	if r.code != 0 {
		t.Fatalf("exit %d, want 0; %s", r.code, r.msg())
	}
	if r.verdict() != want {
		t.Errorf("output %q, want dependabot_only=%s; %s", r.out, want, r.msg())
	}
}

func wantFailClosed(t *testing.T, r doRun) {
	t.Helper()
	if r.code != 1 {
		t.Errorf("exit %d, want 1; %s", r.code, r.msg())
	}
	if r.out != "" {
		t.Errorf("wrote output %q on a failed lookup", r.out)
	}
}

func TestDependabotOnly_PR499ShapeIsTrue(t *testing.T) {
	t.Parallel()
	wantVerdict(t, runDO(t, doOpts{pages: doPage(botCommit())}), "true")
}

func TestDependabotOnly_OneHumanCommitIsFalse(t *testing.T) {
	t.Parallel()
	wantVerdict(t, runDO(t, doOpts{pages: doPage(botCommit(), humanCommit())}), "false")
}

func TestDependabotOnly_ReadsEveryPage(t *testing.T) {
	t.Parallel()
	first, last := botCommit(), botCommit()
	last.sha = "shahead"
	r := runDO(t, doOpts{pages: [][]doCommit{{first}, {humanCommit(), last}}})
	wantVerdict(t, r, "false")
	if len(r.calls) != 1 || !strings.Contains(r.calls[0], "--paginate") || !strings.Contains(r.calls[0], "repos/o/r/pulls/12/commits") {
		t.Errorf("gh calls = %q, want one api call with --paginate and repos/o/r/pulls/12/commits", r.calls)
	}
	wantVerdict(t, runDO(t, doOpts{pages: [][]doCommit{{first}, {last}}}), "true")
}

func TestDependabotOnly_UnverifiedBotCommitIsFalse(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct{ verified, raw string }{
		"false":             {verified: "false"},
		"verified null":     {verified: "null"},
		"verified absent":   {verified: ""},
		"verification null": {raw: "x"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			bc := botCommit()
			bc.verified = c.verified
			bc.sha = "shahead"
			o := doOpts{pages: [][]doCommit{{bc}}}
			if c.raw != "" {
				o = doOpts{raw: `[{"sha":"shahead","author":{"login":"dependabot[bot]"},"committer":{"login":"web-flow"},"commit":{"verification":null}}]`}
			}
			wantVerdict(t, runDO(t, o), "false")
		})
	}
}

func TestDependabotOnly_SelfSignedForgedAuthorIsFalse(t *testing.T) {
	t.Parallel()
	c := botCommit()
	c.committer = doHuman
	wantVerdict(t, runDO(t, doOpts{pages: doPage(c)}), "false")
}

func TestDependabotOnly_ForgedAuthorFromAHumanSenderIsFalse(t *testing.T) {
	t.Parallel()
	wantVerdict(t, runDO(t, doOpts{pages: doPage(botCommit()), env: map[string]string{"EVENT_SENDER": doHuman}}), "false")
	wantVerdict(t, runDO(t, doOpts{pages: doPage(botCommit()), env: map[string]string{"EVENT_SENDER": doBot}}), "true")
}

func TestDependabotOnly_ReopenedAndReadyForReviewNeverSkip(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"reopened", "ready_for_review", "edited", ""} {
		t.Run("action="+action, func(t *testing.T) {
			t.Parallel()
			wantVerdict(t, runDO(t, doOpts{pages: doPage(botCommit()), env: map[string]string{"EVENT_ACTION": action}}), "false")
		})
	}
}

func TestDependabotOnly_OpenedAndSynchronizeSkip(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"opened", "synchronize"} {
		t.Run("action="+action, func(t *testing.T) {
			t.Parallel()
			wantVerdict(t, runDO(t, doOpts{pages: doPage(botCommit()), env: map[string]string{"EVENT_ACTION": action}}), "true")
		})
	}
}

func TestDependabotOnly_RecreateAfterReopenSkipsAgain(t *testing.T) {
	t.Parallel()
	wantVerdict(t, runDO(t, doOpts{pages: doPage(botCommit()), env: map[string]string{"EVENT_ACTION": "reopened"}}), "false")
	rewritten := botCommit()
	rewritten.sha = "shanew"
	wantVerdict(t, runDO(t, doOpts{
		pages: [][]doCommit{{rewritten}},
		env:   map[string]string{"EVENT_ACTION": "synchronize", "HEAD_SHA": "shanew"},
	}), "true")
}

func TestDependabotOnly_LogsTheVerdict(t *testing.T) {
	t.Parallel()
	r := runDO(t, doOpts{pages: doPage(botCommit())})
	wantVerdict(t, r, "true")
	if want := "dependabot-only: true (author=dependabot[bot] sender=dependabot[bot] action=synchronize commits=1)"; !strings.Contains(r.stdout, want) {
		t.Errorf("stdout lacks %q: %s", want, r.msg())
	}
	r = runDO(t, doOpts{pages: doPage(botCommit(), humanCommit())})
	wantVerdict(t, r, "false")
	for _, want := range []string{"dependabot-only: false", "commits=2"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout lacks %q: %s", want, r.msg())
		}
	}
}

func TestDependabotOnly_NullAuthorIsHuman(t *testing.T) {
	t.Parallel()
	c := botCommit()
	c.author = ""
	wantVerdict(t, runDO(t, doOpts{pages: doPage(c)}), "false")
}

func TestDependabotOnly_LoginMustBeExact(t *testing.T) {
	t.Parallel()
	for _, login := range []string{"Dependabot[bot]", "dependabot", "dependabot[bot]x", "dependabot-preview[bot]"} {
		t.Run(login, func(t *testing.T) {
			t.Parallel()
			c := botCommit()
			c.author = login
			wantVerdict(t, runDO(t, doOpts{pages: doPage(c)}), "false")
		})
	}
	wantVerdict(t, runDO(t, doOpts{pages: doPage(botCommit())}), "true")
}

func TestDependabotOnly_LookupFailureFailsClosed(t *testing.T) {
	t.Parallel()
	wantFailClosed(t, runDO(t, doOpts{fail: true}))
}

func TestDependabotOnly_EmptyListFailsClosed(t *testing.T) {
	t.Parallel()
	wantFailClosed(t, runDO(t, doOpts{raw: "[]"}))
}

func TestDependabotOnly_StaleHeadFailsClosed(t *testing.T) {
	t.Parallel()
	wantFailClosed(t, runDO(t, doOpts{pages: doPage(botCommit(), botCommit()), env: map[string]string{"HEAD_SHA": "shaother"}}))
}

func TestDependabotOnly_StaleThenFreshListRetriesToTheVerdict(t *testing.T) {
	t.Parallel()
	stale := `[` + botCommit().json() + `]` // sha "" != HEAD_SHA
	fresh := botCommit()
	fresh.sha = "shahead"
	r := runDO(t, doOpts{raw: stale, then: "[" + fresh.json() + "]"})
	wantVerdict(t, r, "true")
	if len(r.calls) != 2 {
		t.Errorf("gh calls = %d, want 2", len(r.calls))
	}
}

func TestDependabotOnly_StaleListFailsClosedAfterThreeTries(t *testing.T) {
	t.Parallel()
	r := runDO(t, doOpts{raw: `[` + botCommit().json() + `]`})
	wantFailClosed(t, r)
	if len(r.calls) != 3 {
		t.Errorf("gh calls = %d, want 3", len(r.calls))
	}
}

func TestDependabotOnly_MalformedJSONFailsClosed(t *testing.T) {
	t.Parallel()
	wantFailClosed(t, runDO(t, doOpts{raw: "not json"}))
}

func TestDependabotOnly_HumanAuthorIsFalseEvenOnBotCommits(t *testing.T) {
	t.Parallel()
	wantVerdict(t, runDO(t, doOpts{pages: doPage(botCommit()), env: map[string]string{"PR_AUTHOR": doHuman}}), "false")
}

func TestDependabotOnly_HumanAuthorLookupFailureWarnsAndIsFalse(t *testing.T) {
	t.Parallel()
	r := runDO(t, doOpts{fail: true, env: map[string]string{"PR_AUTHOR": doHuman}})
	wantVerdict(t, r, "false")
	if !strings.Contains(r.stdout+r.stderr, "::warning::") {
		t.Errorf("no ::warning:: line: %s", r.msg())
	}
}

func TestDependabotOnly_MissingEnvExits2(t *testing.T) {
	t.Parallel()
	wantVerdict(t, runDO(t, doOpts{pages: doPage(botCommit())}), "true") // the base input is valid
	for _, k := range doEnvKeys {
		t.Run("unset "+k, func(t *testing.T) {
			t.Parallel()
			r := runDO(t, doOpts{pages: doPage(botCommit()), omit: []string{k}})
			wantExit2(t, r)
		})
	}
	t.Run("no argument", func(t *testing.T) {
		t.Parallel()
		wantExit2(t, runDO(t, doOpts{pages: doPage(botCommit()), noArg: true}))
	})
}

func wantExit2(t *testing.T, r doRun) {
	t.Helper()
	if r.code != 2 {
		t.Errorf("exit %d, want 2; %s", r.code, r.msg())
	}
	if r.out != "" {
		t.Errorf("wrote output %q", r.out)
	}
	if len(r.calls) != 0 {
		t.Errorf("gh called: %q", r.calls)
	}
}

func TestDependabotOnly_CommitterLoginMustBeExactWebFlow(t *testing.T) {
	t.Parallel()
	for _, login := range []string{"Web-Flow", "web-flow ", "web-flow[bot]", "web_flow", ""} {
		t.Run("committer="+login, func(t *testing.T) {
			t.Parallel()
			c := botCommit()
			c.committer = login
			wantVerdict(t, runDO(t, doOpts{pages: doPage(c)}), "false")
		})
	}
	wantVerdict(t, runDO(t, doOpts{pages: doPage(botCommit())}), "true")
}

func TestDependabotOnly_VerifiedMustBeBooleanTrue(t *testing.T) {
	t.Parallel()
	for _, v := range []string{`"true"`, "1", `"verified"`, "[]"} {
		t.Run("verified="+v, func(t *testing.T) {
			t.Parallel()
			c := botCommit()
			c.verified = v
			wantVerdict(t, runDO(t, doOpts{pages: doPage(c)}), "false")
		})
	}
}

func TestDependabotOnly_HeadMatchingANonLastCommitFailsClosed(t *testing.T) {
	t.Parallel()
	first, last := botCommit(), botCommit()
	first.sha, last.sha = "shahead", "shalater"
	wantFailClosed(t, runDO(t, doOpts{pages: [][]doCommit{{first, last}}}))
	last.sha = "shahead"
	first.sha = "shafirst"
	wantVerdict(t, runDO(t, doOpts{pages: [][]doCommit{{first, last}}}), "true")
}

func TestDependabotOnly_BadCommitOnAMiddlePageIsFalse(t *testing.T) {
	t.Parallel()
	a, b, c := botCommit(), botCommit(), botCommit()
	b.committer = doHuman
	c.sha = "shahead"
	r := runDO(t, doOpts{pages: [][]doCommit{{a}, {b}, {c}}})
	wantVerdict(t, r, "false")
	if !strings.Contains(r.stdout, "commits=3") {
		t.Errorf("stdout lacks commits=3: %s", r.msg())
	}
	wantVerdict(t, runDO(t, doOpts{pages: [][]doCommit{{a}, {botCommit()}, {c}}}), "true")
}

func TestDependabotOnly_ReadsNoCommitDataFromTheEventPayload(t *testing.T) {
	t.Parallel()
	payload := filepath.Join(t.TempDir(), "event.json")
	forged := `{"commits":[{"author":{"login":"dependabot[bot]"},"committer":{"login":"web-flow"}}],"pull_request":{"commits":1}}`
	if err := os.WriteFile(payload, []byte(forged), 0o644); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"GITHUB_EVENT_PATH": payload}
	wantVerdict(t, runDO(t, doOpts{pages: doPage(botCommit(), humanCommit()), env: env}), "false")
	wantVerdict(t, runDO(t, doOpts{pages: doPage(botCommit()), env: env}), "true")
}

func TestDependabotOnly_HumanAuthorWithAnUnreliableListIsFalseAndLogsTheCount(t *testing.T) {
	t.Parallel()
	human := map[string]string{"PR_AUTHOR": doHuman}
	r := runDO(t, doOpts{raw: "[]", env: human})
	wantVerdict(t, r, "false")
	if !strings.Contains(r.stdout, "commits=0") {
		t.Errorf("stdout lacks commits=0: %s", r.msg())
	}
	human["HEAD_SHA"] = "shaother"
	r = runDO(t, doOpts{pages: doPage(botCommit(), botCommit()), env: human})
	wantVerdict(t, r, "false")
	if !strings.Contains(r.stdout, "commits=2") {
		t.Errorf("stdout lacks commits=2: %s", r.msg())
	}
	r = runDO(t, doOpts{raw: "not json", env: map[string]string{"PR_AUTHOR": doHuman}})
	wantVerdict(t, r, "false")
	if !strings.Contains(r.stdout+r.stderr, "::warning::") {
		t.Errorf("no ::warning:: line: %s", r.msg())
	}
}

func TestDependabotOnly_PRAuthorMustBeExactBot(t *testing.T) {
	t.Parallel()
	for _, author := range []string{"Dependabot[bot]", "dependabot", "dependabot[bot]x", "dependabot-preview[bot]"} {
		t.Run(author, func(t *testing.T) {
			t.Parallel()
			wantVerdict(t, runDO(t, doOpts{pages: doPage(botCommit()), env: map[string]string{"PR_AUTHOR": author}}), "false")
		})
	}
}

func TestDependabotOnly_ActionMustBeExact(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"Opened", "SYNCHRONIZE", "opened ", "synchronized", "labeled"} {
		t.Run("action="+action, func(t *testing.T) {
			t.Parallel()
			wantVerdict(t, runDO(t, doOpts{pages: doPage(botCommit()), env: map[string]string{"EVENT_ACTION": action}}), "false")
		})
	}
}

func TestDependabotOnly_ABotListFailureNeverWritesOutput(t *testing.T) {
	t.Parallel()
	for name, o := range map[string]doOpts{
		"object body":      {raw: `{"message":"Bad credentials"}`},
		"commit not array": {raw: `"x"`},
		"empty body":       {raw: " "},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			wantFailClosed(t, runDO(t, o))
		})
	}
}
