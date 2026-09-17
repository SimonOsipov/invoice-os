package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./internal/tools/mutationreplay <rows.jsonl>")
		os.Exit(2)
	}

	restored, err := Recover(".")
	for _, p := range restored {
		fmt.Fprintf(os.Stderr, "mutationreplay: WARNING restored %s from a backup left by an interrupted run\n", p)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "mutationreplay: recover backups: %v\n", err)
		os.Exit(2)
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		s := <-sigs
		killChild()
		if err := Abort(); err != nil {
			fmt.Fprintf(os.Stderr, "mutationreplay: %v: restore FAILED: %v\n", s, err)
			os.Exit(3)
		}
		fmt.Fprintf(os.Stderr, "mutationreplay: %v: restored, exiting\n", s)
		os.Exit(130)
	}()

	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "mutationreplay: %v\n", err)
		os.Exit(2)
	}

	counts := map[Verdict]int{}
	total := 0
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		total++
		row, err := ParseRow(line)
		label := row.AC
		if label == "" {
			label = fmt.Sprintf("line %d", n)
		}
		var res Result
		if err != nil {
			res = Result{Invalid, fmt.Sprintf("line %d: %v", n, err)}
		} else if res, err = Replay(".", row, runTest); err != nil {
			fmt.Fprintf(os.Stderr, "mutationreplay: FATAL %v\n", err)
			os.Exit(3)
		}
		counts[res.Verdict]++
		fmt.Printf("%s %s — %s\n", res.Verdict, label, res.Reason)
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "mutationreplay: read rows: %v\n", err)
		os.Exit(2)
	}

	fmt.Printf("%d rows: %d proven, %d not proven, %d invalid\n", total, counts[Proven], counts[NotProven], counts[Invalid])
	// Zero rows proves nothing, so it is not a pass.
	if total == 0 || counts[Proven] != total {
		os.Exit(1)
	}
}

var (
	childMu sync.Mutex
	child   *exec.Cmd
)

// killChild kills the whole process group: go test and vitest both fork workers.
func killChild() {
	childMu.Lock()
	defer childMu.Unlock()
	if child != nil && child.Process != nil {
		_ = syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
	}
}

func runCmd(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	childMu.Lock()
	err := cmd.Start()
	if err == nil {
		child = cmd
	}
	childMu.Unlock()
	if err != nil {
		return "", err
	}
	_ = cmd.Wait() // a red test exits non-zero; the outcome comes from the output
	childMu.Lock()
	child = nil
	childMu.Unlock()
	return out.String(), nil
}

func runTest(r Row, k Kind) (Outcome, error) {
	if k == KindGo {
		out, err := runCmd(".", "go", "test", "-count=1", "-p", "1", "-v",
			"-run", GoRunPattern(r.Test), "./"+filepath.ToSlash(filepath.Dir(r.TestFile)))
		return GoOutcome(out, r.Test), err
	}

	pkg, err := nearestPackage(filepath.Dir(r.TestFile))
	if err != nil {
		return NotRun, err
	}
	rel, err := filepath.Rel(pkg, r.TestFile)
	if err != nil {
		return NotRun, err
	}
	// vitest reports real paths; resolve symlinks so the report match holds.
	abs, err := filepath.Abs(r.TestFile)
	if err == nil {
		abs, err = filepath.EvalSymlinks(abs)
	}
	if err != nil {
		return NotRun, err
	}
	tmp, err := os.CreateTemp("", "mutationreplay-*.json")
	if err != nil {
		return NotRun, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	// -t is a regexp over the full name; the report match below is exact.
	out, err := runCmd(pkg, "pnpm", "exec", "vitest", "run", rel,
		"-t", regexp.QuoteMeta(r.Test), "--reporter=json", "--outputFile="+tmp.Name())
	if err != nil {
		return NotRun, err
	}
	report, err := os.ReadFile(tmp.Name())
	if err != nil {
		return NotRun, err
	}
	if len(bytes.TrimSpace(report)) == 0 {
		fmt.Fprintf(os.Stderr, "mutationreplay: vitest wrote no report: %s\n", lastLines(out, 5))
	}
	return VitestOutcome(report, abs, r.Test), nil
}

func nearestPackage(dir string) (string, error) {
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "package.json")); err == nil {
			return d, nil
		}
		if d == "." || d == string(filepath.Separator) {
			return "", fmt.Errorf("no package.json above %s", dir)
		}
	}
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}
