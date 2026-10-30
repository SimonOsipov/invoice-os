// nightly_prod_sha_script_test.go runs scripts/ci/nightly-prod-sha.sh against real git repos.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func prodShaGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func prodShaCommit(t *testing.T, dir string, files ...string) string {
	t.Helper()
	for _, f := range files {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		prev, _ := os.ReadFile(p)
		writeFile(t, p, string(prev)+"x\n")
		prodShaGit(t, dir, "add", f)
	}
	prodShaGit(t, dir, "commit", "-m", "c")
	return prodShaGit(t, dir, "rev-parse", "HEAD")
}

// prodShaMerge merges a side branch that changes files into main with a merge commit.
func prodShaMerge(t *testing.T, dir string, files ...string) string {
	t.Helper()
	prodShaGit(t, dir, "checkout", "-b", "side")
	prodShaCommit(t, dir, files...)
	prodShaGit(t, dir, "checkout", "main")
	prodShaGit(t, dir, "merge", "--no-ff", "-m", "merge", "side")
	prodShaGit(t, dir, "branch", "-D", "side")
	return prodShaGit(t, dir, "rev-parse", "HEAD")
}

func newProdShaRepos(t *testing.T) (origin, work string) {
	t.Helper()
	origin, work = t.TempDir(), t.TempDir()
	prodShaGit(t, origin, "init", "-b", "main")
	prodShaGit(t, work, "init", "-b", "work")
	prodShaGit(t, work, "remote", "add", "origin", origin)
	return origin, work
}

func runProdSha(t *testing.T, work string) (out string, code int) {
	t.Helper()
	script := filepath.Join(repoRoot(t), "scripts", "ci", "nightly-prod-sha.sh")
	outFile := filepath.Join(t.TempDir(), "out")
	cmd := exec.Command("bash", script)
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "GITHUB_OUTPUT="+outFile)
	_, _, err := runToFiles(t, cmd, false)
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(outFile)
	return string(raw), code
}

func TestNightlyProdSha_HeadIsCodeCommit(t *testing.T) {
	t.Parallel()
	origin, work := newProdShaRepos(t)
	prodShaCommit(t, origin, "README.md")
	head := prodShaCommit(t, origin, "a.go")
	out, code := runProdSha(t, work)
	if want := "head=" + head + "\nexpected=" + head + "\n"; code != 0 || out != want {
		t.Errorf("exit %d output %q, want exit 0 output %q", code, out, want)
	}
}

func TestNightlyProdSha_DocsOnlyHeadIsSkipped(t *testing.T) {
	t.Parallel()
	origin, work := newProdShaRepos(t)
	code1 := prodShaCommit(t, origin, "a.go")
	head := prodShaMerge(t, origin, "docs/x.md", "README.md", ".claude/rules/y.md")
	out, code := runProdSha(t, work)
	if want := "head=" + head + "\nexpected=" + code1 + "\n"; code != 0 || out != want {
		t.Errorf("exit %d output %q, want exit 0 output %q", code, out, want)
	}
}

func TestNightlyProdSha_MixedMergeIsExpected(t *testing.T) {
	t.Parallel()
	origin, work := newProdShaRepos(t)
	prodShaCommit(t, origin, "a.go")
	head := prodShaMerge(t, origin, "a.md", "b.go")
	out, code := runProdSha(t, work)
	if want := "head=" + head + "\nexpected=" + head + "\n"; code != 0 || out != want {
		t.Errorf("exit %d output %q, want exit 0 output %q", code, out, want)
	}
}

func TestNightlyProdSha_FetchFailureWritesNothing(t *testing.T) {
	t.Parallel()
	work := t.TempDir()
	prodShaGit(t, work, "init", "-b", "work")
	out, code := runProdSha(t, work)
	if code == 0 || out != "" {
		t.Errorf("exit %d output %q, want non-zero exit and no output", code, out)
	}
}

func TestNightlyProdSha_StaleRefAfterFailedFetchWritesNothing(t *testing.T) {
	t.Parallel()
	origin, work := newProdShaRepos(t)
	prodShaCommit(t, origin, "a.go")
	prodShaGit(t, work, "fetch", "origin", "main")
	if _, code := runProdSha(t, work); code != 0 {
		t.Fatalf("control run with a reachable origin exited %d", code)
	}
	prodShaGit(t, work, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone"))
	out, code := runProdSha(t, work)
	if code == 0 || out != "" {
		t.Errorf("exit %d output %q, want non-zero exit and no output when the fetch fails but origin/main is cached", code, out)
	}
}
