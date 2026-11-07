package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// repo writes files (path to content), tracks all but those listed in untracked.
func repo(t *testing.T, files map[string]string, untracked ...string) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	for p, c := range files {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	skip := map[string]bool{}
	for _, u := range untracked {
		skip[u] = true
	}
	for p := range files {
		if !skip[p] {
			git(t, dir, "add", p)
		}
	}
	return dir
}

func rule(globs ...string) string {
	s := "---\npaths:\n"
	for _, g := range globs {
		s += "  - " + g + "\n"
	}
	return s + "---\nbody\n"
}

func do(dir string) (int, string, string) {
	var o, e bytes.Buffer
	code := run(dir, &o, &e)
	return code, o.String(), e.String()
}

func dead(file, glob string) string {
	return fmt.Sprintf("%s: glob %q matches no tracked file — fix or remove it, or the rule never loads.", file, glob)
}

func shape(file, reason string) string {
	return fmt.Sprintf("%s: %s — paths must be a list of quoted globs without brace groups.", file, reason)
}

func TestRun_AllGlobsMatchPasses(t *testing.T) {
	dir := repo(t, map[string]string{
		".claude/rules/a.md": rule(`"*"`, `"**"`, `"dir/*.go"`),
		".claude/rules/b.md": "no front matter\n",
		"top.txt":            "",
		"dir/x.go":           "",
	})
	code, out, errs := do(dir)
	want := "ruleglobs: 3 globs in 2 rule files all match a tracked file\n"
	if code != 0 || out != want || errs != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errs)
	}
}

func TestRun_AllGlobsMatchPasses_ParserRows(t *testing.T) {
	rows := map[string]struct {
		body  string
		globs int
	}{
		"single quotes":   {"---\npaths:\n  - 'x.go'\n---\n", 1},
		"trailing":        {"---\npaths:\n  - \"x.go\"  # why\n---\n", 1},
		"blank, comment":  {"---\npaths:\n\n  # note\n  - \"x.go\"\n\n---\n", 1},
		"crlf":            {"---\r\npaths:\r\n  - \"x.go\"\r\n---\r\n", 1},
		"four spaces":     {"---\npaths:\n    - \"x.go\"\n---\n", 1},
		"zero indent":     {"---\npaths:\n- \"x.go\"\n---\n", 1},
		"duplicate":       {"---\npaths:\n  - \"x.go\"\n  - \"x.go\"\n---\n", 2},
		"other key after": {"---\npaths:\n  - \"x.go\"\nname: y\n---\n", 1},
	}
	for name, r := range rows {
		t.Run(name, func(t *testing.T) {
			dir := repo(t, map[string]string{".claude/rules/a.md": r.body, "x.go": ""})
			code, out, errs := do(dir)
			want := fmt.Sprintf("ruleglobs: %d globs in 1 rule files all match a tracked file\n", r.globs)
			if code != 0 || out != want {
				t.Fatalf("code=%d out=%q err=%q", code, out, errs)
			}
		})
	}
	t.Run("nested rule file", func(t *testing.T) {
		dir := repo(t, map[string]string{".claude/rules/sub/a.md": rule(`"x.go"`), "x.go": ""})
		if code, out, errs := do(dir); code != 0 || !strings.Contains(out, "1 globs in 1 rule files") {
			t.Fatalf("code=%d out=%q err=%q", code, out, errs)
		}
	})
}

func TestRun_DeadGlobFailsNamingFileAndGlob(t *testing.T) {
	dir := repo(t, map[string]string{".claude/rules/a.md": rule(`"nope/**"`), "x.go": ""})
	code, out, errs := do(dir)
	if code != 1 || out != "" || errs != dead(".claude/rules/a.md", "nope/**")+"\n" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errs)
	}
}

func TestRun_ReportsEveryDeadGlob(t *testing.T) {
	dir := repo(t, map[string]string{
		".claude/rules/a.md":     rule(`"gone/**"`, `"x.go"`),
		".claude/rules/sub/b.md": rule(`"a/[b"`),
		"x.go":                   "",
	})
	code, _, errs := do(dir)
	if code != 1 {
		t.Fatalf("code=%d err=%q", code, errs)
	}
	for _, w := range []string{dead(".claude/rules/a.md", "gone/**"), dead(".claude/rules/sub/b.md", "a/[b")} {
		if !strings.Contains(errs, w) {
			t.Errorf("missing %q in %q", w, errs)
		}
	}
	if strings.Contains(errs, `"x.go"`) {
		t.Errorf("live glob reported: %q", errs)
	}
}

func TestRun_UntrackedMatchDoesNotCount(t *testing.T) {
	dir := repo(t, map[string]string{".claude/rules/a.md": rule(`"x.go"`), "x.go": ""}, "x.go")
	if code, _, errs := do(dir); code != 1 {
		t.Fatalf("code=%d err=%q", code, errs)
	}
}

func TestRun_StarDoesNotCrossSlash(t *testing.T) {
	for glob, want := range map[string]int{"d/*.go": 1, "d/**": 0} {
		dir := repo(t, map[string]string{".claude/rules/a.md": rule(`"` + glob + `"`), "d/sub/x.go": ""})
		if code, _, errs := do(dir); code != want {
			t.Errorf("%s: code=%d want %d err=%q", glob, code, want, errs)
		}
	}
}

func TestRun_NoPathsKeyPasses(t *testing.T) {
	dir := repo(t, map[string]string{
		".claude/rules/a.md": "---\nname: x\n---\nbody\n",
		".claude/rules/b.md": "just text\n",
		".claude/rules/c.md": "---\nname: x\n---\npaths:\n  - \"nope\"\n",
	})
	code, out, errs := do(dir)
	if code != 0 || out != "ruleglobs: 0 globs in 3 rule files all match a tracked file\n" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errs)
	}
}

func TestRun_ShapeErrorsFail(t *testing.T) {
	rows := map[string]struct{ body, reason string }{
		"no items":      {"---\npaths:\n---\n", "paths list is empty"},
		"empty flow":    {"---\npaths: []\n---\n", "paths list is empty"},
		"scalar":        {"---\npaths: \"x\"\n---\n", "paths is a scalar or flow list"},
		"flow list":     {"---\npaths: [\"a/**\", \"b/*.go\"]\n---\n", "paths is a scalar or flow list"},
		"unquoted":      {"---\npaths:\n  - x.go\n---\n", `item "- x.go" is not a quoted string`},
		"braces":        {"---\npaths:\n  - \"a/{b,c}\"\n---\n", `glob "a/{b,c}" holds a brace group`},
		"empty glob":    {"---\npaths:\n  - \"\"\n---\n", "a glob is empty"},
		"only comments": {"---\npaths:\n  # x\n---\n", "paths list is empty"},
		"unclosed":      {"---\npaths:\n  - \"x.go\"\n", "front matter never closes"},
	}
	for name, r := range rows {
		t.Run(name, func(t *testing.T) {
			dir := repo(t, map[string]string{".claude/rules/a.md": r.body, "x.go": ""})
			code, _, errs := do(dir)
			if code != 1 || errs != shape(".claude/rules/a.md", r.reason)+"\n" {
				t.Fatalf("code=%d err=%q", code, errs)
			}
		})
	}
}

func TestRun_RenameDropsLastMatch(t *testing.T) {
	dir := repo(t, map[string]string{".claude/rules/a.md": rule(`"src/*.go"`), "src/a.go": ""})
	if code, _, errs := do(dir); code != 0 {
		t.Fatalf("before: code=%d err=%q", code, errs)
	}
	git(t, dir, "mv", "src/a.go", "lib_a.go")
	code, _, errs := do(dir)
	if code != 1 || !strings.Contains(errs, dead(".claude/rules/a.md", "src/*.go")) {
		t.Fatalf("after: code=%d err=%q", code, errs)
	}
}

func TestRun_NoRuleFilesIsExit2(t *testing.T) {
	dir := repo(t, map[string]string{"x.go": ""})
	if code, _, _ := do(dir); code != 2 {
		t.Fatalf("code=%d", code)
	}
}

func TestRun_NotAGitRepoIsExit2(t *testing.T) {
	if code, _, _ := do(t.TempDir()); code != 2 {
		t.Fatalf("code=%d", code)
	}
}

func TestRun_UnreadableRuleFileIsExit2(t *testing.T) {
	dir := repo(t, map[string]string{".claude/rules/a.md": rule(`"x.go"`), "x.go": ""})
	if err := os.Remove(filepath.Join(dir, ".claude/rules/a.md")); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := do(dir); code != 2 || out != "" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestRun_GitFailureOnAGlobIsExit2(t *testing.T) {
	dir := repo(t, map[string]string{".claude/rules/a.md": rule(`"../outside"`)})
	if code, out, errs := do(dir); code != 2 || out != "" || !strings.Contains(errs, "../outside") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errs)
	}
}

func TestRun_ShapeAndDeadGlobReportedTogether(t *testing.T) {
	dir := repo(t, map[string]string{
		".claude/rules/a.md": "---\npaths: []\n---\n",
		".claude/rules/b.md": rule(`"gone"`),
	})
	code, _, errs := do(dir)
	if code != 1 || !strings.Contains(errs, shape(".claude/rules/a.md", "paths list is empty")) || !strings.Contains(errs, dead(".claude/rules/b.md", "gone")) {
		t.Fatalf("code=%d err=%q", code, errs)
	}
}
