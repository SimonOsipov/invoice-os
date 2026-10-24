// lane_test.go runs scripts/ci/lane.sh against temp git repos and a fake gh.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const ()

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
	writeShim(t, filepath.Join(dir, "gh"), gh)
	return laneGH{dir: dir}
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
	t.Parallel()
	if got := laneClassify(t, "frontend/library/src/App.tsx", "frontend/library/package.json"); got != "library" {
		t.Fatalf("classify = %q, want library", got)
	}
}

var sharedWithBlog = []string{
	"frontend/library/src/components/SceneView.tsx", "frontend/library/src/icons.tsx", "frontend/library/src/player.ts",
	"frontend/library/src/scene.ts", "frontend/library/src/timing.ts", "frontend/library/src/types.ts",
}

func TestLane_ALibraryFileTheBlogImportsGivesTheFullLane(t *testing.T) {
	for _, p := range sharedWithBlog {
		t.Run(p, func(t *testing.T) {
			if got := laneClassify(t, p); got != "full" {
				t.Fatalf("classify %s = %q, want full", p, got)
			}
			if got := laneClassify(t, "frontend/library/src/App.tsx", p); got != "full" {
				t.Fatalf("classify with App.tsx before %s = %q, want full", p, got)
			}
		})
	}
}

func TestLane_OtherLibraryFilesKeepTheSmallLane(t *testing.T) {
	for _, p := range []string{"frontend/library/src/components/Home.tsx", "frontend/library/src/scene.test.ts", "frontend/library/src/components/Player.tsx"} {
		if got := laneClassify(t, p); got != "library" {
			t.Fatalf("classify %s = %q, want library", p, got)
		}
	}
}

func TestLane_ASharedPathMatchesExactly(t *testing.T) {
	for p, want := range map[string]string{
		"frontend/library/src/scene.tsx":     "library",
		"frontend/library/src/timing.ts.bak": "library",
		"x/frontend/library/src/scene.ts":    "full",
	} {
		if got := laneClassify(t, p); got != want {
			t.Fatalf("classify %s = %q, want %s", p, got, want)
		}
	}
}

func TestLane_BlogOnlyPathsTakeTheSmallLane(t *testing.T) {
	if got := laneClassify(t, "frontend/blog/src/App.tsx", "frontend/blog/package.json"); got != "blog" {
		t.Fatalf("classify = %q, want blog", got)
	}
	for _, near := range []string{"frontend/blog/src/index.html", "frontend/blog/Dockerfile.bak", "frontend/blog/index.html.map"} {
		if got := laneClassify(t, near); got != "blog" {
			t.Fatalf("classify %s = %q, want blog: only the three server inputs take the full lane", near, got)
		}
	}
}

func TestLane_BlogServerInputsGiveTheFullLane(t *testing.T) {
	for _, in := range []string{"frontend/blog/index.html", "frontend/blog/Dockerfile", "frontend/blog/Dockerfile.dockerignore"} {
		t.Run(in, func(t *testing.T) {
			if got := laneClassify(t, in); got != "full" {
				t.Fatalf("classify %s = %q, want full", in, got)
			}
			if got := laneClassify(t, in, "frontend/blog/src/App.tsx"); got != "full" {
				t.Fatalf("classify %s with App.tsx = %q, want full", in, got)
			}
			if got := laneClassify(t, "frontend/blog/src/App.tsx", in); got != "full" {
				t.Fatalf("classify App.tsx with %s = %q, want full", in, got)
			}
		})
	}
}

func TestLane_LibraryAndBlogTogetherGiveTheFullLane(t *testing.T) {
	for _, order := range [][]string{
		{"frontend/library/a.ts", "frontend/blog/b.ts"},
		{"frontend/blog/b.ts", "frontend/library/a.ts"},
		{"frontend/blog/b.ts", "frontend/blog/c.ts", "frontend/library/a.ts"},
	} {
		if got := laneClassify(t, order...); got != "full" {
			t.Fatalf("classify %v = %q, want full", order, got)
		}
	}
}

func TestLane_AnyOtherPathGivesTheFullLane(t *testing.T) {
	t.Parallel()
	for _, other := range []string{
		"pnpm-lock.yaml", "packages/monitoring/src/x.ts", "Caddyfile", "scripts/ci/lane.sh",
		".github/workflows/ci.yml", "e2e/smoke/apps.ts", "frontend/app/src/App.tsx",
	} {
		t.Run(other, func(t *testing.T) {
			if got := laneClassify(t, "frontend/library/src/App.tsx", other); got != "full" {
				t.Fatalf("classify with %s = %q, want full", other, got)
			}
			if got := laneClassify(t, "frontend/blog/src/App.tsx", other); got != "full" {
				t.Fatalf("classify blog with %s = %q, want full", other, got)
			}
		})
	}
}

func TestLane_PrefixMatchesOnAPathBoundary(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"frontend/libraryx/a.ts", "frontend/library", "x/frontend/library/a.ts", "frontend/library-foo/a.ts", "frontend/Library/a.ts",
		"frontend/blogx/a.ts", "frontend/blog", "x/frontend/blog/a.ts", "frontend/blog-foo/a.ts", "frontend/Blog/a.ts"} {
		t.Run(p, func(t *testing.T) {
			if got := laneClassify(t, p); got != "full" {
				t.Fatalf("classify %s = %q, want full", p, got)
			}
		})
	}
}

func TestLane_NoPathGivesTheFullLane(t *testing.T) {
	t.Parallel()
	if got := laneClassify(t); got != "full" {
		t.Fatalf("classify of an empty list = %q, want full", got)
	}
}

func TestLane_OnlyANamedPackageTakesTheSmallLane(t *testing.T) {
	t.Parallel()
	smallLane := map[string]string{"library": "library", "blog": "blog"}
	pkgs, err := filepath.Glob(filepath.Join(repoRoot(t), "frontend", "*", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, p := range pkgs {
		dirs = append(dirs, filepath.Base(filepath.Dir(p)))
	}
	if len(dirs) < 6 {
		t.Fatalf("found %d frontend packages %v, want at least 6", len(dirs), dirs)
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
		if lane, ok := smallLane[d]; ok {
			want = lane
		}
		if got := laneClassify(t, "frontend/"+d+"/src/main.tsx"); got != want {
			t.Errorf("frontend/%s classifies %q, want %q", d, got, want)
		}
	}
}

func TestLanePR_LibraryOnlyMergeIsTheSmallLane(t *testing.T) {
	t.Parallel()
	r := newLaneRepo(t)
	r.mergeBranch(t, "frontend/library/a.ts")
	out, code, ghOut := runLane(t, r.dir, nil, "", "pr")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	requireLine(t, out, "lane=library")
	requireLine(t, ghOut, "lane=library")
}

func TestLanePR_ASharedLibraryFileMergeIsTheFullLane(t *testing.T) {
	r := newLaneRepo(t)
	r.mergeBranch(t, "frontend/library/src/timing.ts")
	out, code, ghOut := runLane(t, r.dir, nil, "", "pr")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	requireLine(t, out, "lane=full")
	requireLine(t, ghOut, "lane=full")
}

func TestLanePR_BlogOnlyMergeIsTheSmallLane(t *testing.T) {
	r := newLaneRepo(t)
	r.mergeBranch(t, "frontend/blog/a.ts")
	out, code, ghOut := runLane(t, r.dir, nil, "", "pr")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	requireLine(t, out, "lane=blog")
	requireLine(t, ghOut, "lane=blog")
}

func TestLanePR_ABlogServerInputMergeIsTheFullLane(t *testing.T) {
	r := newLaneRepo(t)
	r.mergeBranch(t, "frontend/blog/index.html")
	out, code, ghOut := runLane(t, r.dir, nil, "", "pr")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	requireLine(t, out, "lane=full")
	requireLine(t, ghOut, "lane=full")
}

func TestLanePR_MixedMergeIsTheFullLane(t *testing.T) {
	t.Parallel()
	r := newLaneRepo(t)
	r.mergeBranch(t, "frontend/library/a.ts", "frontend/app/b.ts")
	out, _, ghOut := runLane(t, r.dir, nil, "", "pr")
	requireLine(t, out, "lane=full")
	requireLine(t, ghOut, "lane=full")
}

func TestLanePR_AMoveIntoTheLibraryIsTheFullLane(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	r := newLaneRepo(t)
	r.commit(t, "only", "frontend/library/a.ts")
	out, code, ghOut := runLane(t, r.dir, nil, "", "pr")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	requireLine(t, ghOut, "lane=full")
}

func TestLanePRE2E_CombinesTheFilterWithTheLane(t *testing.T) {
	t.Parallel()
	cases := []struct {
		filter, path, wantLane, wantE2E string
	}{
		{"true", "frontend/library/a.ts", "library", "false"},
		{"true", "frontend/blog/a.ts", "blog", "false"},
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
	t.Parallel()
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

func TestLane_UsageErrors(t *testing.T) {
	t.Parallel()
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

func TestLanePR_DeletesAndRenamesAreJudgedByEveryPath(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, from, to, want string }{
		{"delete inside the library", "frontend/library/a.ts", "", "library"},
		{"rename inside the library", "frontend/library/a.ts", "frontend/library/z.ts", "library"},
		{"rename out of the library", "frontend/library/a.ts", "frontend/app/a.ts", "full"},
		{"delete outside the library", "frontend/app/b.ts", "", "full"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newLaneRepo(t)
			r.commit(t, "base", "README.md", "frontend/app/b.ts", "frontend/library/a.ts")
			r.git(t, "checkout", "-q", "-b", "feature")
			if c.to == "" {
				r.git(t, "rm", "-q", c.from)
			} else {
				if err := os.MkdirAll(filepath.Join(r.dir, filepath.Dir(c.to)), 0o755); err != nil {
					t.Fatal(err)
				}
				r.git(t, "mv", c.from, c.to)
			}
			r.git(t, "commit", "-q", "-m", "change")
			r.git(t, "checkout", "-q", "main")
			r.git(t, "merge", "-q", "--no-ff", "-m", "merge", "feature")
			out, code, ghOut := runLane(t, r.dir, nil, "", "pr")
			if code != 0 {
				t.Fatalf("exit %d: %s", code, out)
			}
			requireLine(t, ghOut, "lane="+c.want)
		})
	}
}
