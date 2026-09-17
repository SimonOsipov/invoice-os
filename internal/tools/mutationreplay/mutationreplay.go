// Package main implements mutationreplay: it re-applies each recorded
// "this break turns this test red" claim and reports the ones that stay green.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Row is one recorded mutation claim.
type Row struct {
	AC       string `json:"ac"`
	File     string `json:"file"`
	Find     string `json:"find"`
	Replace  string `json:"replace"`
	TestFile string `json:"test_file"`
	Test     string `json:"test"`
}

// ParseRow decodes one JSONL line. Unknown fields are errors so a misspelt
// key fails the row instead of silently emptying a field.
func ParseRow(line []byte) (Row, error) {
	var r Row
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return r, err
	}
	for name, v := range map[string]string{"ac": r.AC, "file": r.File, "find": r.Find, "test_file": r.TestFile, "test": r.Test} {
		if v == "" {
			return r, fmt.Errorf("missing field %q", name)
		}
	}
	return r, nil
}

type Kind int

const (
	KindGo Kind = iota + 1
	KindVitest
)

const playwrightReason = "Playwright specs cannot replay locally; cite the deploy-gate run instead"

// DetectKind picks the runner from the test file name. Under e2e/, *.spec.ts
// is Playwright and vitest only includes *.test.ts (e2e/vitest.config.ts).
func DetectKind(testFile string) (Kind, error) {
	p := filepath.ToSlash(testFile)
	switch {
	case strings.HasSuffix(p, "_test.go"):
		return KindGo, nil
	case strings.HasSuffix(p, ".test.ts"), strings.HasSuffix(p, ".test.tsx"):
		return KindVitest, nil
	case strings.HasPrefix(p, "e2e/") && (strings.HasSuffix(p, ".spec.ts") || strings.HasSuffix(p, ".spec.tsx")):
		return 0, fmt.Errorf("%s", playwrightReason)
	}
	return 0, fmt.Errorf("unsupported test file extension: %s", testFile)
}

// CountNeedle counts overlapping occurrences, so "aa" in "aaa" is ambiguous.
func CountNeedle(src []byte, needle string) int {
	if needle == "" {
		return 0
	}
	n := 0
	for i := 0; i <= len(src); {
		j := bytes.Index(src[i:], []byte(needle))
		if j < 0 {
			break
		}
		n++
		i += j + 1
	}
	return n
}

// Check validates a row against the source it mutates and returns the runner kind.
func Check(r Row, src []byte) (Kind, error) {
	if r.Find == r.Replace {
		return 0, fmt.Errorf("find == replace; the mutation changes nothing")
	}
	if c := CountNeedle(src, r.Find); c != 1 {
		return 0, fmt.Errorf("find occurs %d times in %s, want exactly 1", c, r.File)
	}
	return DetectKind(r.TestFile)
}

type Outcome int

const (
	NotRun Outcome = iota
	BuildFailed
	Passed
	Failed
)

// goName applies the one rewrite of testing's subtest naming that matters in
// practice: spaces become underscores.
func goName(test string) string { return strings.ReplaceAll(test, " ", "_") }

// GoRunPattern anchors every level of a (sub)test name for `go test -run`.
func GoRunPattern(test string) string {
	parts := strings.Split(goName(test), "/")
	for i, p := range parts {
		parts[i] = "^" + regexp.QuoteMeta(p) + "$"
	}
	return strings.Join(parts, "/")
}

// GoOutcome reads `go test -v` output for the named test's own result line.
func GoOutcome(output, test string) Outcome {
	name := goName(test)
	res := NotRun
	for _, line := range strings.Split(output, "\n") {
		l := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(l, "--- FAIL: "+name+" ("):
			return Failed
		case strings.HasPrefix(l, "--- PASS: "+name+" ("):
			res = Passed
		}
	}
	if res == NotRun && (strings.Contains(output, "[build failed]") || strings.Contains(output, "[setup failed]")) {
		return BuildFailed
	}
	return res
}

type vitestReport struct {
	TestResults []struct {
		Name             string `json:"name"`
		Status           string `json:"status"`
		AssertionResults []struct {
			Title    string `json:"title"`
			FullName string `json:"fullName"`
			Status   string `json:"status"`
		} `json:"assertionResults"`
	} `json:"testResults"`
}

// VitestOutcome reads a vitest JSON report. A test matches on its full name or
// its bare title; every match must pass for Passed, any failure is Failed.
func VitestOutcome(report []byte, absFile, test string) Outcome {
	var rep vitestReport
	if err := json.Unmarshal(report, &rep); err != nil {
		return BuildFailed
	}
	fileFailed := false
	matched, passed := 0, 0
	for _, tr := range rep.TestResults {
		if filepath.Clean(tr.Name) != filepath.Clean(absFile) {
			continue
		}
		if tr.Status == "failed" {
			fileFailed = true
		}
		for _, a := range tr.AssertionResults {
			if a.FullName != test && a.Title != test {
				continue
			}
			matched++
			switch a.Status {
			case "failed":
				return Failed
			case "passed":
				passed++
			}
		}
	}
	if matched > 0 && passed == matched {
		return Passed
	}
	// A failed file with no matching result is a transform or import error.
	if fileFailed {
		return BuildFailed
	}
	return NotRun
}

type Verdict string

const (
	Proven    Verdict = "PROVEN"
	NotProven Verdict = "NOT-PROVEN"
	Invalid   Verdict = "INVALID"
)

type Result struct {
	Verdict Verdict
	Reason  string
}

// Runner runs the row's named test against the tree as it currently is on disk.
type Runner func(r Row, k Kind) (Outcome, error)

// Replay proves one row. A non-nil error means a mutated file could not be
// restored and the caller must stop.
func Replay(root string, r Row, run Runner) (Result, error) {
	path := filepath.Join(root, r.File)
	src, err := os.ReadFile(path)
	if err != nil {
		return Result{Invalid, fmt.Sprintf("cannot read file: %v", err)}, nil
	}
	if _, err := os.Stat(filepath.Join(root, r.TestFile)); err != nil {
		return Result{Invalid, fmt.Sprintf("cannot read test_file: %v", err)}, nil
	}
	kind, err := Check(r, src)
	if err != nil {
		return Result{Invalid, err.Error()}, nil
	}

	control, err := run(r, kind)
	if err != nil {
		return Result{Invalid, fmt.Sprintf("control run: %v", err)}, nil
	}
	switch control {
	case NotRun:
		return Result{Invalid, "control not green: filter matched nothing"}, nil
	case BuildFailed:
		return Result{Invalid, "control not green: build failed on unmodified code"}, nil
	case Failed:
		return Result{Invalid, "control not green: test fails on unmodified code"}, nil
	}

	mutated := bytes.Replace(src, []byte(r.Find), []byte(r.Replace), 1)
	m, err := Mutate(root, r.File, src, mutated)
	if err != nil {
		return Result{}, fmt.Errorf("mutate %s: %w", r.File, err)
	}
	out, runErr := run(r, kind)
	if err := m.Restore(); err != nil {
		return Result{}, err
	}
	if runErr != nil {
		return Result{Invalid, fmt.Sprintf("mutated run: %v", runErr)}, nil
	}
	switch out {
	case Failed:
		return Result{Proven, "test failed under mutation"}, nil
	case Passed:
		return Result{NotProven, "test stayed green under mutation"}, nil
	}
	return Result{NotProven, "mutation broke the build, not the test"}, nil
}

// BackupDir holds the original bytes of any file currently mutated.
const BackupDir = ".ralph/mutation-backup"

// mu serialises every write to a mutated file, including a signal-time Abort.
var (
	mu     sync.Mutex
	active *Mutation
)

type Mutation struct {
	root, path string
	original   []byte
	backup     string
	done       bool
}

func backupPath(root, path string) string {
	return filepath.Join(root, BackupDir, url.PathEscape(filepath.ToSlash(path)))
}

// Mutate writes the backup to disk before touching the file, so a crash
// between the two is recoverable by Recover.
func Mutate(root, path string, original, mutated []byte) (*Mutation, error) {
	mu.Lock()
	defer mu.Unlock()
	m := &Mutation{root: root, path: path, original: original, backup: backupPath(root, path)}
	if err := os.MkdirAll(filepath.Dir(m.backup), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(m.backup, original, 0o644); err != nil {
		return nil, err
	}
	active = m
	if err := os.WriteFile(filepath.Join(root, path), mutated, 0o644); err != nil {
		return nil, fmt.Errorf("%w (backup kept at %s)", err, m.backup)
	}
	return m, nil
}

// Restore writes the original bytes back, verifies them by hash, then drops the backup.
func (m *Mutation) Restore() error {
	mu.Lock()
	defer mu.Unlock()
	return m.restoreLocked()
}

func (m *Mutation) restoreLocked() error {
	if m.done {
		return nil
	}
	full := filepath.Join(m.root, m.path)
	if err := os.WriteFile(full, m.original, 0o644); err != nil {
		return fmt.Errorf("restore %s: %w (backup kept at %s)", m.path, err, m.backup)
	}
	got, err := os.ReadFile(full)
	if err != nil || sha256.Sum256(got) != sha256.Sum256(m.original) {
		return fmt.Errorf("restore %s: hash mismatch after write (backup kept at %s)", m.path, m.backup)
	}
	if err := os.Remove(m.backup); err != nil {
		return fmt.Errorf("restored %s but could not remove backup: %w", m.path, err)
	}
	m.done = true
	active = nil
	return nil
}

// Abort restores any in-flight mutation for a signal handler. It leaves mu
// locked so no later write lands before the process exits.
func Abort() error {
	mu.Lock()
	if active == nil {
		return nil
	}
	return active.restoreLocked()
}

// Recover restores every file left mutated by a crashed run and returns their paths.
func Recover(root string) ([]string, error) {
	dir := filepath.Join(root, BackupDir)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var restored []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path, err := url.PathUnescape(e.Name())
		if err != nil {
			return restored, fmt.Errorf("backup %s: %w", e.Name(), err)
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return restored, err
		}
		m := &Mutation{root: root, path: filepath.FromSlash(path), original: b, backup: filepath.Join(dir, e.Name())}
		if err := m.Restore(); err != nil {
			return restored, err
		}
		restored = append(restored, path)
	}
	return restored, nil
}
