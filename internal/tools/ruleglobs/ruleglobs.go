package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// item is a quoted list entry with an optional trailing comment.
var item = regexp.MustCompile(`^ *- +(?:"([^"]*)"|'([^']*)') *(?:#.*)?$`)

var errUnclosed = errors.New("front matter never closes")

// shapeError is a malformed paths list; reason fills the failure copy.
type shapeError string

func (e shapeError) Error() string { return string(e) }

func fence(l string) bool { return strings.TrimRight(l, " \t") == "---" }

// parseGlobs returns the paths globs of a rule file; none for an always-loaded file.
func parseGlobs(data []byte) ([]string, error) {
	text := strings.TrimPrefix(string(data), "\ufeff")
	lines := strings.Split(strings.ReplaceAll(text, "\r", ""), "\n")
	if !fence(lines[0]) {
		return nil, nil
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if fence(lines[i]) {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, shapeError(errUnclosed.Error())
	}
	fm := lines[1:end]
	for i, l := range fm {
		if !strings.HasPrefix(l, "paths:") {
			continue
		}
		v := strings.TrimSpace(strings.TrimPrefix(l, "paths:"))
		if strings.HasPrefix(v, "#") {
			v = ""
		}
		if v != "" {
			if v == "[]" {
				return nil, shapeError("paths list is empty")
			}
			return nil, shapeError("paths is a scalar or flow list")
		}
		var globs []string
		for _, l := range fm[i+1:] {
			t := strings.TrimSpace(l)
			if t == "" || strings.HasPrefix(t, "#") {
				continue
			}
			m := item.FindStringSubmatch(l)
			if m == nil {
				if strings.HasPrefix(strings.TrimLeft(l, " "), "- ") {
					return nil, shapeError(fmt.Sprintf("item %q is not a quoted string", t))
				}
				break
			}
			g := m[1] + m[2]
			if g == "" {
				return nil, shapeError("a glob is empty")
			}
			if strings.ContainsAny(g, "{}") {
				return nil, shapeError(fmt.Sprintf("glob %q holds a brace group", g))
			}
			globs = append(globs, g)
		}
		if len(globs) == 0 {
			return nil, shapeError("paths list is empty")
		}
		return globs, nil
	}
	return nil, nil
}

// tracked lists the tracked files git matches for one glob. git treats a
// wildcard-free glob as a folder prefix, so only an exact path counts there.
func tracked(dir, glob string) (int, []byte, error) {
	cmd := exec.Command("git", "ls-files", "-z", "--", ":(glob)"+glob)
	cmd.Dir = dir
	out, err := cmd.Output()
	if !strings.ContainsAny(glob, "*?[") {
		n := 0
		for _, p := range bytes.Split(out, []byte{0}) {
			if string(p) == glob {
				n++
			}
		}
		return n, out, err
	}
	return bytes.Count(out, []byte{0}), out, err
}

func run(dir string, stdout, stderr io.Writer) int {
	_, list, err := tracked(dir, ".claude/rules/**/*.md")
	if err != nil {
		fmt.Fprintf(stderr, "ruleglobs: git ls-files: %v\n", err)
		return 2
	}
	var files []string
	for _, p := range bytes.Split(list, []byte{0}) {
		if len(p) > 0 {
			files = append(files, string(p))
		}
	}
	if len(files) == 0 {
		fmt.Fprintln(stderr, "ruleglobs: no tracked rule file under .claude/rules")
		return 2
	}
	var problems []string
	gitFailed := false
	total := 0
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			fmt.Fprintf(stderr, "ruleglobs: %v\n", err)
			return 2
		}
		globs, err := parseGlobs(data)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %s — paths must be a list of quoted globs without brace groups.", f, err))
			continue
		}
		for _, g := range globs {
			total++
			n, _, err := tracked(dir, g)
			if err != nil {
				problems = append(problems, fmt.Sprintf("ruleglobs: git ls-files %q: %v", g, err))
				gitFailed = true
				continue
			}
			if n == 0 {
				problems = append(problems, fmt.Sprintf("%s: glob %q matches no tracked file — fix or remove it, or the rule never loads.", f, g))
			}
		}
	}
	if len(problems) > 0 {
		fmt.Fprintln(stderr, strings.Join(problems, "\n"))
		if gitFailed {
			return 2
		}
		return 1
	}
	fmt.Fprintf(stdout, "ruleglobs: %d globs in %d rule files all match a tracked file\n", total, len(files))
	return 0
}
