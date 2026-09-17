// Package main implements breaklist, the one repo search a plan cites when it
// lists every place a change will break.
//
//	go run ./internal/tools/breaklist '<Go RE2 regexp>' [path ...]
//
// It walks files itself instead of shelling out to grep, because grep's traps
// undercount silently: `git grep -E` ignores `\b`, grep skips any file holding a
// NUL byte as binary, and a whole-repo scan walks sibling worktrees. Every hit is
// printed; nothing is truncated. A self-check (file-count floor and a NUL-byte
// canary) runs before the result is reported.
package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// skipDirs are vendored, generated, or sibling-checkout trees.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "dist": true, "build": true, "coverage": true,
	".claude": true, ".ralph": true, "playwright-report": true, "test-results": true,
}

// skipDirPrefixes match per-suite report dirs such as e2e/report-smoke.
var skipDirPrefixes = []string{"report-"}

// mediaExts are skipped by extension only; every other file is read in full.
var mediaExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".ico": true,
	".pdf": true, ".woff": true, ".woff2": true, ".ttf": true, ".otf": true,
	".zip": true, ".gz": true, ".xlsx": true,
}

// canaryDir holds the NUL-byte fixture. It is excluded from normal results.
const canaryDir = "internal/tools/breaklist/testdata"

const maxLineLen = 300

// Hit is one matching line.
type Hit struct {
	Path string
	Line int
	Text string
}

// Result is a completed search.
type Result struct {
	Hits    []Hit
	Files   int // files with at least one hit
	Scanned int // files read
}

func skipDir(path, name string) bool {
	if skipDirs[name] || strings.HasSuffix(filepath.ToSlash(path), canaryDir) {
		return true
	}
	for _, p := range skipDirPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// Search walks roots and matches re against every line of every file. A walk
// root itself is never skipped, so an explicit path into a skipped dir works.
// Any read error aborts: a silently unread file is an undercount.
func Search(re *regexp.Regexp, roots []string) (Result, error) {
	var res Result
	seen := map[string]bool{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path != root && skipDir(path, d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || mediaExts[strings.ToLower(filepath.Ext(path))] {
				return nil
			}
			clean := filepath.Clean(path)
			if seen[clean] {
				return nil
			}
			seen[clean] = true
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			res.Scanned++
			hits := matchLines(re, clean, data)
			if len(hits) > 0 {
				res.Files++
				res.Hits = append(res.Hits, hits...)
			}
			return nil
		})
		if err != nil {
			return res, err
		}
	}
	return res, nil
}

func matchLines(re *regexp.Regexp, path string, data []byte) []Hit {
	var hits []Hit
	for i, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if re.Match(line) {
			hits = append(hits, Hit{Path: path, Line: i + 1, Text: display(line)})
		}
	}
	return hits
}

// display makes a line safe to print: NUL bytes shown as \0, long lines cut on
// a rune boundary.
func display(line []byte) string {
	s := strings.ReplaceAll(string(line), "\x00", `\0`)
	if len(s) <= maxLineLen {
		return s
	}
	r := []rune(s)
	if len(r) <= maxLineLen {
		return s
	}
	return string(r[:maxLineLen]) + " …[trimmed]"
}
