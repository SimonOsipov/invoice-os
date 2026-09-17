package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeRepo builds a root with a valid canary so run's self-check passes.
func fakeRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, filepath.Join(canaryDir, canaryFile), "bin\x00ary\n"+canaryText+"\n")
	return root
}

func runIn(t *testing.T, cfg config, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, cfg, &out, &errb)
	return code, out.String(), errb.String()
}

func paths(hits []Hit) []string {
	var out []string
	for _, h := range hits {
		out = append(out, filepath.ToSlash(h.Path))
	}
	return out
}

func TestSearch_ReadsFileWithNULByte(t *testing.T) {
	root := t.TempDir()
	write(t, root, "route.test.ts", "import x\x00\nparseLocation('/a')\nparseLocation('/b')\n")

	res, err := Search(regexp.MustCompile(`parseLocation`), []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 2 || res.Hits[0].Line != 2 || res.Hits[1].Line != 3 {
		t.Fatalf("hits = %+v, want lines 2 and 3", res.Hits)
	}
}

func TestSearch_WordBoundary(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "parse()\nparseLocation()\nreparse()\nx := parse\n")

	res, err := Search(regexp.MustCompile(`\bparse\b`), []string{root})
	if err != nil {
		t.Fatal(err)
	}
	var lines []int
	for _, h := range res.Hits {
		lines = append(lines, h.Line)
	}
	if fmt.Sprint(lines) != "[1 4]" {
		t.Fatalf("\\b matched lines %v, want [1 4]", lines)
	}
}

func TestSearch_SkipsExcludedDirsAndMedia(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/keep.ts", "needle\n")
	for _, rel := range []string{
		".git/x", "node_modules/p/x.js", "frontend/app/dist/x.js", "build/x", "coverage/x",
		".claude/worktrees/x/src/keep.ts", ".ralph/log", "e2e/playwright-report/x.html",
		"e2e/report-smoke/x.html", "e2e/test-results/x", canaryDir + "/x.txt", "img/logo.PNG",
	} {
		write(t, root, rel, "needle\n")
	}

	res, err := Search(regexp.MustCompile(`needle`), []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(res.Hits); len(got) != 1 || !strings.HasSuffix(got[0], "src/keep.ts") {
		t.Fatalf("hits in %v, want only src/keep.ts", got)
	}
	if res.Scanned != 1 {
		t.Fatalf("scanned %d files, want 1", res.Scanned)
	}
}

func TestSearch_ExplicitRootInsideSkippedDirIsWalked(t *testing.T) {
	root := t.TempDir()
	write(t, root, "node_modules/p/x.js", "needle\n")

	res, err := Search(regexp.MustCompile(`needle`), []string{filepath.Join(root, "node_modules")})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(res.Hits))
	}
}

func TestRun_PrintsEveryHitWithoutTruncation(t *testing.T) {
	root := fakeRepo(t)
	const n = 2500
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "hit %d\n", i)
	}
	write(t, root, "a/big.txt", b.String())
	write(t, root, "b/one.txt", "hit\n")

	code, out, stderr := runIn(t, config{root: root, floor: 1}, `^hit`)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != n+2 {
		t.Fatalf("printed %d lines, want %d hits + TOTAL", len(lines), n+1)
	}
	if want := fmt.Sprintf("TOTAL %d hits in 2 files (2 files scanned)", n+1); lines[len(lines)-1] != want {
		t.Fatalf("last line %q, want %q", lines[len(lines)-1], want)
	}
}

func TestRun_ZeroHitsExitsZero(t *testing.T) {
	root := fakeRepo(t)
	write(t, root, "src/a.ts", "x\n")
	code, out, _ := runIn(t, config{root: root, floor: 1}, `absent`)
	if code != 0 || out != "TOTAL 0 hits in 0 files (1 files scanned)\n" {
		t.Fatalf("exit %d, out %q", code, out)
	}
}

func TestRun_BadRegexpExitsOne(t *testing.T) {
	code, _, _ := runIn(t, config{root: fakeRepo(t), floor: 1}, `(unclosed`)
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
}

func TestRun_FloorFailureExitsTwo(t *testing.T) {
	root := fakeRepo(t)
	code, out, stderr := runIn(t, config{root: root, floor: 5}, `x`)
	if code != 2 || out != "" || !strings.Contains(stderr, "floor is 5") {
		t.Fatalf("exit %d, out %q, stderr %q", code, out, stderr)
	}
}

func TestRun_FloorIgnoredForExplicitPath(t *testing.T) {
	root := fakeRepo(t)
	write(t, root, "src/a.ts", "x\n")
	code, _, stderr := runIn(t, config{root: root, floor: 1000}, `x`, filepath.Join(root, "src"))
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
}

func TestRun_CanaryFailuresExitTwo(t *testing.T) {
	cases := map[string]string{
		"missing":     "",
		"no NUL byte": canaryText + "\n",
		"no canary":   "bin\x00ary\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if content != "" {
				write(t, root, filepath.Join(canaryDir, canaryFile), content)
			}
			write(t, root, "src/a.ts", "x\n")
			code, out, stderr := runIn(t, config{root: root, floor: 1}, `x`)
			if code != 2 || out != "" || !strings.Contains(stderr, "SELF-CHECK FAILED") {
				t.Fatalf("exit %d, out %q, stderr %q", code, out, stderr)
			}
		})
	}
}

// TestRun_RealCanaryPasses runs the committed fixture through the self-check.
func TestRun_RealCanaryPasses(t *testing.T) {
	if err := checkCanary("../../.."); err != nil {
		t.Fatal(err)
	}
}

func TestDisplay_TrimsLongLinesAndShowsNUL(t *testing.T) {
	if got := display([]byte("a\x00b")); got != `a\0b` {
		t.Fatalf("got %q", got)
	}
	long := strings.Repeat("é", maxLineLen+50)
	got := display([]byte(long))
	if !strings.HasPrefix(got, strings.Repeat("é", maxLineLen)+" ") || !strings.HasSuffix(got, "[trimmed]") {
		t.Fatalf("trimmed line = %q", got)
	}
}
