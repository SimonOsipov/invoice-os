// lane_test.go runs scripts/ci/lane.sh against temp git repos and a fake gh.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	laneSelfRun = 900

	laneGatewayJob = "Deploy gateway → persistent env"
	laneFleetJob   = "Fleet /healthz gate (all 10 backends green)"
	laneSPAJob     = "SPA build gate (push/dispatch)"
)

type laneJob struct {
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
}

func laneFullChain() []laneJob {
	return []laneJob{
		{laneGatewayJob, "success"}, {laneFleetJob, "success"}, {laneSPAJob, "success"},
	}
}

// laneChainWith returns a full chain in which the job named by prefix has conclusion c.
func laneChainWith(prefix, c string) []laneJob {
	jobs := laneFullChain()
	for i := range jobs {
		if strings.HasPrefix(jobs[i].Name, prefix) {
			jobs[i].Conclusion = c
		}
	}
	return jobs
}

type laneRun struct {
	ID      int
	Sha     string
	Event   string
	Branch  string
	Started string
	Jobs    []laneJob
}

// lanePushRun is a main push run with a fixed start time per id, so a higher id is newer.
func lanePushRun(id int, sha string, jobs []laneJob) laneRun {
	return laneRun{ID: id, Sha: sha, Event: "push", Branch: "main", Started: fmt.Sprintf("2026-10-01T10:%02d:00Z", id%60), Jobs: jobs}
}

// laneGH is a fake gh on PATH. It answers the runs list from runs.out and a jobs read from
// jobs-<id>.out; a sibling .fail file makes that call exit 1 with its content on stderr.
type laneGH struct{ dir string }

func newLaneGH(t *testing.T) laneGH {
	t.Helper()
	dir := t.TempDir()
	gh := `#!/bin/sh
dir='` + dir + `'
printf '%s\n' "$*" >> "$dir/argv.log"
case "$*" in
  *actions/workflows/dev-env.yml/runs*) key=runs ;;
  *actions/runs/*/jobs*) key=jobs-$(printf '%s' "$*" | sed -n 's#.*actions/runs/\([0-9][0-9]*\)/jobs.*#\1#p') ;;
  *) echo "gh stub: unrouted call: $*" >&2; exit 98 ;;
esac
if [ -f "$dir/$key.fail" ]; then cat "$dir/$key.fail" >&2; exit 1; fi
if [ -f "$dir/$key.out" ]; then cat "$dir/$key.out"; exit 0; fi
echo "gh stub: no answer for $key" >&2
exit 97
`
	writeFile(t, filepath.Join(dir, "gh"), gh)
	if err := os.Chmod(filepath.Join(dir, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	return laneGH{dir: dir}
}

// serve answers the runs list with runs and each run's jobs read with its Jobs.
func (g laneGH) serve(t *testing.T, runs ...laneRun) {
	t.Helper()
	var list []map[string]any
	for _, r := range runs {
		list = append(list, map[string]any{
			"id": r.ID, "head_sha": r.Sha, "event": r.Event, "head_branch": r.Branch, "run_started_at": r.Started,
		})
		g.put(t, fmt.Sprintf("jobs-%d.out", r.ID), laneJSON(t, map[string]any{"jobs": r.Jobs}))
	}
	g.put(t, "runs.out", laneJSON(t, map[string]any{"workflow_runs": list}))
}

func laneJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (g laneGH) put(t *testing.T, name, body string) {
	t.Helper()
	writeFile(t, filepath.Join(g.dir, name), body+"\n")
}

func (g laneGH) argv(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(g.dir, "argv.log"))
	if err != nil {
		t.Fatalf("gh was never called: %v", err)
	}
	return string(b)
}

type laneRepo struct{ dir string }

func newLaneRepo(t *testing.T) laneRepo {
	t.Helper()
	r := laneRepo{dir: t.TempDir()}
	r.git(t, "init", "-q", "-b", "main")
	return r
}

func (r laneRepo) git(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	cmd.Dir = r.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes each path with the message as its content and commits; it returns the sha.
func (r laneRepo) commit(t *testing.T, msg string, paths ...string) string {
	t.Helper()
	for _, p := range paths {
		full := filepath.Join(r.dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, full, msg+" "+p+"\n")
	}
	r.git(t, append([]string{"add", "--"}, paths...)...)
	r.git(t, "commit", "-q", "-m", msg)
	return r.git(t, "rev-parse", "HEAD")
}

// mergeBranch builds the PR merge commit: a base commit, a branch that edits paths, then a --no-ff merge.
func (r laneRepo) mergeBranch(t *testing.T, paths ...string) {
	t.Helper()
	r.commit(t, "base", "README.md", "frontend/app/b.ts")
	r.git(t, "checkout", "-q", "-b", "feature")
	r.commit(t, "edit", paths...)
	r.git(t, "checkout", "-q", "main")
	r.git(t, "merge", "-q", "--no-ff", "-m", "merge", "feature")
}

// runLane runs lane.sh in dir with extra env lines and returns stdout+stderr, the exit code and the $GITHUB_OUTPUT content.
func runLane(t *testing.T, dir string, gh *laneGH, env string, args ...string) (out string, code int, ghOut string) {
	t.Helper()
	script := filepath.Join(repoRoot(t), "scripts", "ci", "lane.sh")
	info, err := os.Stat(script)
	if err != nil {
		t.Fatalf("scripts/ci/lane.sh does not exist: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("scripts/ci/lane.sh is not executable (mode %v)", info.Mode())
	}
	outFile := filepath.Join(t.TempDir(), "github_output")
	pre := "exec 2>&1\ncd '" + dir + "'\nexport GITHUB_OUTPUT='" + outFile + "'\n"
	if gh != nil {
		pre += "export PATH='" + gh.dir + "':\"$PATH\"\n"
	}
	call := "bash '" + script + "'"
	for _, a := range args {
		call += " '" + a + "'"
	}
	stdout, stderr, code := runBashScript(t, pre+env+call+"\n")
	b, _ := os.ReadFile(outFile)
	return stdout + stderr, code, string(b)
}

func laneClassify(t *testing.T, paths ...string) string {
	t.Helper()
	list := ""
	for _, p := range paths {
		list += p + "\n"
	}
	stdout, stderr, code := runBashScript(t, "printf '%s' '"+list+"' | bash scripts/ci/lane.sh classify")
	if code != 0 {
		t.Fatalf("classify exit %d: %s%s", code, stdout, stderr)
	}
	return strings.TrimSpace(stdout)
}

func requireLine(t *testing.T, text, want string) {
	t.Helper()
	for _, l := range strings.Split(text, "\n") {
		if l == want {
			return
		}
	}
	t.Fatalf("no line %q in:\n%s", want, text)
}

func TestLane_LibraryOnlyPathsTakeTheSmallLane(t *testing.T) {
	if got := laneClassify(t, "frontend/library/src/App.tsx", "frontend/library/package.json"); got != "library" {
		t.Fatalf("classify = %q, want library", got)
	}
}

func TestLane_AnyOtherPathGivesTheFullLane(t *testing.T) {
	for _, other := range []string{
		"pnpm-lock.yaml", "packages/monitoring/src/x.ts", "Caddyfile", "scripts/ci/lane.sh",
		".github/workflows/ci.yml", "e2e/smoke/apps.ts", "frontend/app/src/App.tsx",
	} {
		t.Run(other, func(t *testing.T) {
			if got := laneClassify(t, "frontend/library/src/App.tsx", other); got != "full" {
				t.Fatalf("classify with %s = %q, want full", other, got)
			}
		})
	}
}

func TestLane_PrefixMatchesOnAPathBoundary(t *testing.T) {
	for _, p := range []string{"frontend/libraryx/a.ts", "frontend/library", "x/frontend/library/a.ts"} {
		t.Run(p, func(t *testing.T) {
			if got := laneClassify(t, p); got != "full" {
				t.Fatalf("classify %s = %q, want full", p, got)
			}
		})
	}
}

func TestLane_NoPathGivesTheFullLane(t *testing.T) {
	if got := laneClassify(t); got != "full" {
		t.Fatalf("classify of an empty list = %q, want full", got)
	}
}

func TestLane_OnlyANamedPackageTakesTheSmallLane(t *testing.T) {
	smallLane := map[string]bool{"library": true}
	pkgs, err := filepath.Glob(filepath.Join(repoRoot(t), "frontend", "*", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, p := range pkgs {
		dirs = append(dirs, filepath.Base(filepath.Dir(p)))
	}
	if len(dirs) < 5 {
		t.Fatalf("found %d frontend packages %v, want at least 5", len(dirs), dirs)
	}
	foundApp := false
	for _, d := range dirs {
		foundApp = foundApp || d == "app"
	}
	if !foundApp {
		t.Fatalf("control: frontend/app not found in %v", dirs)
	}
	for _, d := range append(dirs, "zz-new-spa") {
		want := "full"
		if smallLane[d] {
			want = "library"
		}
		if got := laneClassify(t, "frontend/"+d+"/src/main.tsx"); got != want {
			t.Errorf("frontend/%s classifies %q, want %q", d, got, want)
		}
	}
}

func TestLanePR_LibraryOnlyMergeIsTheSmallLane(t *testing.T) {
	r := newLaneRepo(t)
	r.mergeBranch(t, "frontend/library/a.ts")
	out, code, ghOut := runLane(t, r.dir, nil, "", "pr")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	requireLine(t, out, "lane=library")
	requireLine(t, ghOut, "lane=library")
}

func TestLanePR_MixedMergeIsTheFullLane(t *testing.T) {
	r := newLaneRepo(t)
	r.mergeBranch(t, "frontend/library/a.ts", "frontend/app/b.ts")
	out, _, ghOut := runLane(t, r.dir, nil, "", "pr")
	requireLine(t, out, "lane=full")
	requireLine(t, ghOut, "lane=full")
}

func TestLanePR_AMoveIntoTheLibraryIsTheFullLane(t *testing.T) {
	r := newLaneRepo(t)
	r.commit(t, "base", "README.md", "frontend/app/b.ts")
	r.git(t, "checkout", "-q", "-b", "feature")
	if err := os.MkdirAll(filepath.Join(r.dir, "frontend/library"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.git(t, "mv", "frontend/app/b.ts", "frontend/library/b.ts")
	r.git(t, "commit", "-q", "-m", "move")
	r.git(t, "checkout", "-q", "main")
	r.git(t, "merge", "-q", "--no-ff", "-m", "merge", "feature")
	out, _, _ := runLane(t, r.dir, nil, "", "pr")
	requireLine(t, out, "lane=full")
}

func TestLanePR_NoParentIsTheFullLane(t *testing.T) {
	r := newLaneRepo(t)
	r.commit(t, "only", "frontend/library/a.ts")
	out, code, ghOut := runLane(t, r.dir, nil, "", "pr")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	requireLine(t, ghOut, "lane=full")
}

func TestLanePRE2E_CombinesTheFilterWithTheLane(t *testing.T) {
	cases := []struct {
		filter, path, wantLane, wantE2E string
	}{
		{"true", "frontend/library/a.ts", "library", "false"},
		{"true", "frontend/zz-new-spa/src/x.ts", "full", "true"},
		{"true", "frontend/app/b.ts", "full", "true"},
		{"false", "frontend/app/b.ts", "full", "false"},
	}
	for _, c := range cases {
		t.Run(c.filter+" "+c.path, func(t *testing.T) {
			r := newLaneRepo(t)
			r.mergeBranch(t, c.path)
			out, code, ghOut := runLane(t, r.dir, nil, "export FILTER_E2E="+c.filter+"\n", "pr-e2e")
			if code != 0 {
				t.Fatalf("exit %d: %s", code, out)
			}
			requireLine(t, ghOut, "lane="+c.wantLane)
			requireLine(t, ghOut, "e2e="+c.wantE2E)
			requireLine(t, out, "e2e="+c.wantE2E)
		})
	}
}

func TestLanePRE2E_UnknownFilterVerdictFails(t *testing.T) {
	for _, filter := range []string{"", "maybe"} {
		t.Run("filter="+filter, func(t *testing.T) {
			r := newLaneRepo(t)
			r.mergeBranch(t, "frontend/library/a.ts")
			out, code, ghOut := runLane(t, r.dir, nil, "export FILTER_E2E='"+filter+"'\n", "pr-e2e")
			if code != 1 {
				t.Fatalf("exit %d, want 1: %s", code, out)
			}
			if !strings.Contains(out, "::error::") || !strings.Contains(out, "FILTER_E2E") {
				t.Fatalf("no ::error:: naming FILTER_E2E in: %s", out)
			}
			if strings.Contains(ghOut, "e2e=") {
				t.Fatalf("GITHUB_OUTPUT has an e2e= line: %s", ghOut)
			}
		})
	}
}

// pushRepo is F -> X? -> L? -> S with F, L, S named as the tests need.
type pushRepo struct {
	laneRepo
	F, X, L, S string
}

// newPushRepo builds F, then X (edits frontend/app) when withApp, then L (library only) when withLib, then S (library only).
func newPushRepo(t *testing.T, withApp, withLib bool) pushRepo {
	t.Helper()
	p := pushRepo{laneRepo: newLaneRepo(t)}
	p.F = p.commit(t, "full", "README.md", "frontend/library/a.ts")
	if withApp {
		p.X = p.commit(t, "app", "frontend/app/b.ts")
	}
	if withLib {
		p.L = p.commit(t, "lib", "frontend/library/a.ts")
	}
	p.S = p.commit(t, "head", "frontend/library/c.ts")
	return p
}

func (p pushRepo) push(t *testing.T, gh laneGH, sha string) (string, int, string) {
	t.Helper()
	return runLane(t, p.dir, &gh, "export REPO=o/r GH_TOKEN=fake RUN_ID="+fmt.Sprint(laneSelfRun)+"\n", "push", sha)
}

func wantScope(t *testing.T, out string, code int, ghOut, want string) {
	t.Helper()
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	requireLine(t, ghOut, "scope="+want)
	requireLine(t, out, "scope="+want)
}

func TestLanePush_LibraryOnlySinceTheLastFullDeploy(t *testing.T) {
	p := newPushRepo(t, false, true)
	gh := newLaneGH(t)
	gh.serve(t,
		lanePushRun(laneSelfRun, p.S, nil),
		lanePushRun(10, p.F, laneFullChain()),
	)
	out, code, ghOut := p.push(t, gh, p.S)
	wantScope(t, out, code, ghOut, "library")
	if !strings.Contains(out, "run 10") {
		t.Fatalf("reason does not name run 10: %s", out)
	}
}

func TestLanePush_AnOutsidePathGivesFull(t *testing.T) {
	p := newPushRepo(t, true, false)
	gh := newLaneGH(t)
	gh.serve(t, lanePushRun(10, p.F, laneFullChain()))
	out, code, ghOut := p.push(t, gh, p.S)
	wantScope(t, out, code, ghOut, "full")
}

func TestLanePush_ALibraryOnlyDeployNeverCounts(t *testing.T) {
	p := newPushRepo(t, true, true)
	gh := newLaneGH(t)
	gh.serve(t,
		lanePushRun(20, p.L, []laneJob{
			{"Deploy gateway → ${{ needs.prepare-env.outputs.environment }}", "skipped"},
			{"Deploy library (small lane) → persistent env", "success"},
		}),
		lanePushRun(10, p.F, laneFullChain()),
	)
	out, code, ghOut := p.push(t, gh, p.S)
	wantScope(t, out, code, ghOut, "full")
}

func TestLanePush_AFailedOrCancelledGatewayNeverCounts(t *testing.T) {
	for _, c := range []string{"failure", "cancelled"} {
		t.Run(c, func(t *testing.T) {
			p := newPushRepo(t, true, true)
			gh := newLaneGH(t)
			gh.serve(t,
				lanePushRun(20, p.L, laneChainWith("Deploy gateway ", c)),
				lanePushRun(10, p.F, laneFullChain()),
			)
			out, code, ghOut := p.push(t, gh, p.S)
			wantScope(t, out, code, ghOut, "full")
		})
	}
}

func TestLanePush_AGatewayWithAFailedGateIsNotABase(t *testing.T) {
	for _, c := range []struct{ prefix, conclusion string }{
		{"Fleet /healthz gate", "failure"}, {"SPA build gate", "failure"}, {"SPA build gate", "cancelled"},
	} {
		t.Run(c.prefix+" "+c.conclusion, func(t *testing.T) {
			p := newPushRepo(t, true, true)
			gh := newLaneGH(t)
			gh.serve(t,
				lanePushRun(20, p.L, laneChainWith(c.prefix, c.conclusion)),
				lanePushRun(10, p.F, laneFullChain()),
			)
			out, code, ghOut := p.push(t, gh, p.S)
			wantScope(t, out, code, ghOut, "full")
		})
	}
}

func TestLanePush_TheCurrentRunAndNonMainPushRunsNeverCount(t *testing.T) {
	p := newPushRepo(t, true, true)
	gh := newLaneGH(t)
	self := lanePushRun(laneSelfRun, p.L, laneFullChain())
	dispatch := lanePushRun(30, p.L, laneFullChain())
	dispatch.Event = "workflow_dispatch"
	other := lanePushRun(31, p.L, laneFullChain())
	other.Branch = "feature/x"
	for _, r := range []*laneRun{&self, &dispatch, &other} {
		r.Started = "2026-10-01T12:00:00Z"
	}
	gh.serve(t, self, dispatch, other, lanePushRun(10, p.F, laneFullChain()))
	out, code, ghOut := p.push(t, gh, p.S)
	wantScope(t, out, code, ghOut, "full")
}

func TestLanePush_TheNewestStartWins(t *testing.T) {
	p := newPushRepo(t, true, true)
	gh := newLaneGH(t)
	a := lanePushRun(50, p.L, laneFullChain())
	a.Started = "2026-10-01T09:00:00Z"
	b := lanePushRun(40, p.F, laneFullChain())
	b.Started = "2026-10-01T11:00:00Z"
	gh.serve(t, a, b)
	out, code, ghOut := p.push(t, gh, p.S)
	wantScope(t, out, code, ghOut, "full")
}

func TestLanePush_LookupFailureGivesFull(t *testing.T) {
	cases := map[string]func(t *testing.T, p pushRepo, gh laneGH){
		"runs call fails": func(t *testing.T, p pushRepo, gh laneGH) {
			gh.put(t, "runs.fail", "gh: HTTP 502")
		},
		"runs body is not JSON": func(t *testing.T, p pushRepo, gh laneGH) {
			gh.put(t, "runs.out", "<html>bad gateway</html>")
		},
		"jobs call fails": func(t *testing.T, p pushRepo, gh laneGH) {
			gh.serve(t, lanePushRun(10, p.F, laneFullChain()))
			gh.put(t, "jobs-10.fail", "gh: HTTP 502")
			_ = os.Remove(filepath.Join(gh.dir, "jobs-10.out"))
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			p := newPushRepo(t, false, true)
			gh := newLaneGH(t)
			setup(t, p, gh)
			out, code, ghOut := p.push(t, gh, p.S)
			wantScope(t, out, code, ghOut, "full")
			if !strings.Contains(out, "lookup failed") && !strings.Contains(out, "not JSON") {
				t.Fatalf("reason does not name the failure: %s", out)
			}
		})
	}
}

func TestLanePush_NoFullDeployGivesFull(t *testing.T) {
	p := newPushRepo(t, false, true)
	gh := newLaneGH(t)
	gh.serve(t, lanePushRun(10, p.F, []laneJob{{laneGatewayJob, "success"}}))
	out, code, ghOut := p.push(t, gh, p.S)
	wantScope(t, out, code, ghOut, "full")
}

func TestLanePush_BaseNotAnAncestorGivesFull(t *testing.T) {
	t.Run("sibling branch", func(t *testing.T) {
		p := newPushRepo(t, false, true)
		p.git(t, "checkout", "-q", "-b", "side", p.F)
		side := p.commit(t, "side", "frontend/library/side.ts")
		p.git(t, "checkout", "-q", "main")
		gh := newLaneGH(t)
		gh.serve(t, lanePushRun(10, side, laneFullChain()))
		out, code, ghOut := p.push(t, gh, p.S)
		wantScope(t, out, code, ghOut, "full")
	})
	t.Run("absent sha", func(t *testing.T) {
		p := newPushRepo(t, false, true)
		gh := newLaneGH(t)
		gh.serve(t, lanePushRun(10, strings.Repeat("a", 40), laneFullChain()))
		out, code, ghOut := p.push(t, gh, p.S)
		wantScope(t, out, code, ghOut, "full")
	})
}

func TestLanePush_EmptyDiffGivesFull(t *testing.T) {
	p := newPushRepo(t, false, true)
	gh := newLaneGH(t)
	gh.serve(t, lanePushRun(10, p.S, laneFullChain()))
	out, code, ghOut := p.push(t, gh, p.S)
	wantScope(t, out, code, ghOut, "full")
}

func TestLanePush_ReadsOnlyMainPushRuns(t *testing.T) {
	p := newPushRepo(t, false, true)
	gh := newLaneGH(t)
	gh.serve(t, lanePushRun(10, p.F, laneFullChain()))
	p.push(t, gh, p.S)
	var runsCall string
	for _, l := range strings.Split(gh.argv(t), "\n") {
		if strings.Contains(l, "workflows/dev-env.yml/runs") {
			runsCall = l
		}
	}
	for _, want := range []string{"event=push", "branch=main"} {
		if !strings.Contains(runsCall, want) {
			t.Fatalf("runs call %q lacks %s", runsCall, want)
		}
	}
}

func TestLanePush_WritesOneOutputLine(t *testing.T) {
	p := newPushRepo(t, false, true)
	for name, setup := range map[string]func(gh laneGH){
		"library": func(gh laneGH) { gh.serve(t, lanePushRun(10, p.F, laneFullChain())) },
		"full":    func(gh laneGH) { gh.put(t, "runs.fail", "gh: HTTP 502") },
	} {
		t.Run(name, func(t *testing.T) {
			gh := newLaneGH(t)
			setup(gh)
			_, code, ghOut := p.push(t, gh, p.S)
			if code != 0 {
				t.Fatalf("exit %d", code)
			}
			if n := strings.Count(ghOut, "scope="); n != 1 || strings.Count(ghOut, "\n") != 1 {
				t.Fatalf("GITHUB_OUTPUT = %q, want exactly one scope= line", ghOut)
			}
		})
	}
}

func TestLane_UsageErrors(t *testing.T) {
	r := newLaneRepo(t)
	r.commit(t, "x", "README.md")
	gh := newLaneGH(t)
	const env = "export REPO=o/r GH_TOKEN=fake RUN_ID=1\n"
	cases := []struct {
		name string
		env  string
		args []string
	}{
		{"no subcommand", env, nil},
		{"unknown subcommand", env, []string{"bogus"}},
		{"push without sha", env, []string{"push"}},
		{"push without REPO", "export RUN_ID=1\n", []string{"push", "abc"}},
		{"push without RUN_ID", "export REPO=o/r\n", []string{"push", "abc"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, code, _ := runLane(t, r.dir, &gh, "unset REPO RUN_ID\n"+c.env, c.args...)
			if code != 2 || !strings.Contains(out, "::error::usage") {
				t.Fatalf("exit %d, output %q; want exit 2 and ::error::usage", code, out)
			}
		})
	}
}
