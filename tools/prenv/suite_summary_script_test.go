// suite_summary_script_test.go runs scripts/dev/suite-summary.sh on fixture logs.
// The pnpm lines use U+2009 (written \u2009), as the real logs do.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	ts    = "\u2009"
	fl    = "frontend/landing test:  "
	noHit = ": no failing file, package or test named, see .ralph/"
)

// runSummary writes the logs into <tmp>/.ralph and runs the script from <tmp> with a relative log dir.
func runSummary(t *testing.T, logs map[string]string, env []string, suites ...string) (string, int) {
	t.Helper()
	tmp := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmp, ".ralph"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range logs {
		if err := os.WriteFile(filepath.Join(tmp, ".ralph", name+".log"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bash := "bash"
	if _, err := os.Stat("/bin/bash"); err == nil {
		bash = "/bin/bash"
	}
	cmd := exec.Command(bash, append([]string{filepath.Join(repoRoot(t), "scripts/dev/suite-summary.sh"), ".ralph"}, suites...)...)
	cmd.Dir = tmp
	cmd.Env = append(os.Environ(), env...)
	stdout, _, err := runToFiles(t, cmd, false)
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running suite-summary.sh: %v", err)
		}
		code = ee.ExitCode()
	}
	return stdout, code
}

func wantOut(t *testing.T, got string, lines ...string) {
	t.Helper()
	want := strings.Join(lines, "\n") + "\n"
	if got != want {
		t.Fatalf("stdout:\n%s\nwant:\n%s", got, want)
	}
}

func unitOut(t *testing.T, log string) string {
	t.Helper()
	out, _ := runSummary(t, map[string]string{"unit": log}, nil, "unit")
	return out
}

func TestSuiteSummary_NamesEachFailedVitestFileOnceWithItsPackage(t *testing.T) {
	t.Parallel()
	log := fl + " FAIL  src/App.register.dom.test.tsx > a\n" +
		fl + " FAIL  src/App.register.dom.test.tsx > b\n" +
		fl + " FAIL  src/App.reset.dom.test.tsx > a\n" +
		fl + " FAIL  src/App.reset.dom.test.tsx > b\n" +
		fl + " FAIL  src/App.reset.dom.test.tsx > c\n" +
		fl + " ❯ src/App.verify.dom.test.tsx (12 tests | 5 failed) 15311ms\n"
	wantOut(t, unitOut(t, log),
		"unit file: frontend/landing src/App.register.dom.test.tsx",
		"unit file: frontend/landing src/App.reset.dom.test.tsx",
		"unit file: frontend/landing src/App.verify.dom.test.tsx")
}

func TestSuiteSummary_AFileNamedByTheArrowLineAloneIsNamed(t *testing.T) {
	t.Parallel()
	wantOut(t, unitOut(t, fl+" ❯ src/App.invite.dom.test.tsx (4 tests | 4 failed) 6356ms\n"),
		"unit file: frontend/landing src/App.invite.dom.test.tsx")
}

func TestSuiteSummary_AFileNamedByTheFailLineAloneIsNamed(t *testing.T) {
	t.Parallel()
	wantOut(t, unitOut(t, "packages/a test:  FAIL  src/a.test.ts [ src/a.test.ts ]\n"),
		"unit file: packages/a src/a.test.ts")
}

func TestSuiteSummary_UnprefixedVitestLinesAreMatched(t *testing.T) {
	t.Parallel()
	log := " FAIL  seam/a.test.ts > t\n  ❯ seam/b.test.ts (2 tests | 1 failed) 5ms\n"
	wantOut(t, unitOut(t, log), "unit file: seam/a.test.ts", "unit file: seam/b.test.ts")
}

func TestSuiteSummary_StackFramesAreNotFiles(t *testing.T) {
	t.Parallel()
	log := fl + " ❯ src/App.register.dom.test.tsx:834:3\n" + fl + " ❯ onlyNotice src/App.reset.dom.test.tsx:95:28\n"
	wantOut(t, unitOut(t, log), "unit"+noHit+"unit.log")
}

func TestSuiteSummary_TheSameFileNameInTwoPackagesIsTwoEntries(t *testing.T) {
	t.Parallel()
	log := "frontend/landing test:  FAIL  src/App.test.tsx > x\nfrontend/app test:  FAIL  src/App.test.tsx > x\n"
	wantOut(t, unitOut(t, log), "unit file: frontend/landing src/App.test.tsx", "unit file: frontend/app src/App.test.tsx")
}

func TestSuiteSummary_AFailLineWithoutAFileIsNotAFile(t *testing.T) {
	t.Parallel()
	wantOut(t, unitOut(t, fl+" FAIL  Unhandled Rejection\n"), "unit"+noHit+"unit.log")
}

// pnpm 10.32.1 probe lines.
func TestSuiteSummary_NamesAPackageFromAnErrorLineWithThinSpaces(t *testing.T) {
	t.Parallel()
	log := ts + "ERROR" + ts + " @x/a@1.0.0 test: `echo`\nExit status 1\n" +
		ts + "ERROR" + ts + " @x/c@1.0.0 test: `echo`\n" +
		ts + "ERR_PNPM_RECURSIVE_FAIL" + ts + " Summary: 2 fails\n"
	wantOut(t, unitOut(t, log), "unit package: @x/a@1.0.0", "unit package: @x/c@1.0.0")
}

// The bail-form line of bug-52.
func TestSuiteSummary_NamesAPackageFromTheRunFirstFailLine(t *testing.T) {
	t.Parallel()
	log := ts + "ERR_PNPM_RECURSIVE_RUN_FIRST_FAIL" + ts + " @invoice-os/landing@0.1.0 test: `vitest run`\n"
	wantOut(t, unitOut(t, log), "unit package: @invoice-os/landing@0.1.0")
}

func TestSuiteSummary_AsciiSpacesInThePnpmLinesAlsoMatch(t *testing.T) {
	t.Parallel()
	wantOut(t, unitOut(t, " ERROR  @x/a@1.0.0 test: `echo`\n"), "unit package: @x/a@1.0.0")
}

func TestSuiteSummary_APnpmCodeWithoutAPackageNamesNothing(t *testing.T) {
	t.Parallel()
	wantOut(t, unitOut(t, ts+"ERR_PNPM_RECURSIVE_FAIL"+ts+" Summary: 2 fails\n"), "unit"+noHit+"unit.log")
}

func TestSuiteSummary_NamesAGoPackageAndTopLevelTest(t *testing.T) {
	t.Parallel()
	log := "ok  \tgithub.com/x/y/ok\t1s\n--- FAIL: TestA (0.04s)\n    --- FAIL: TestA/sub (0.00s)\nFAIL\n" +
		"FAIL\tgithub.com/x/y/bad\t1.2s\nFAIL\tgithub.com/x/y/broken [build failed]\n"
	out, code := runSummary(t, map[string]string{"go": log}, nil, "go")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	wantOut(t, out, "go package: github.com/x/y/bad", "go package: github.com/x/y/broken [build failed]", "go test: TestA")
}

func TestSuiteSummary_DBLogFollowsTheGoRules(t *testing.T) {
	t.Parallel()
	log := "--- FAIL: TestRLS_StaffReadAuditLogPlansReadAnIndex (0.56s)\nFAIL\tgithub.com/x/platform/db\t26.885s\n"
	out, _ := runSummary(t, map[string]string{"db": log}, nil, "db")
	wantOut(t, out, "db package: github.com/x/platform/db", "db test: TestRLS_StaffReadAuditLogPlansReadAnIndex")
}

func TestSuiteSummary_AGoTestThatPanicsOnATimeoutNamesItsPackage(t *testing.T) {
	t.Parallel()
	out, _ := runSummary(t, map[string]string{"go": "panic: test timed out after 10m0s\nFAIL\tgithub.com/x/slow\t600.1s\n"}, nil, "go")
	wantOut(t, out, "go package: github.com/x/slow")
}

func failingTests(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "--- FAIL: Test%d (0.01s)\n", i)
	}
	return b.String()
}

func TestSuiteSummary_AListOfExactlyTheCapHasNoMoreLine(t *testing.T) {
	t.Parallel()
	out, _ := runSummary(t, map[string]string{"go": failingTests(25)}, nil, "go")
	if n := strings.Count(out, "go test: Test"); n != 25 || strings.Contains(out, "more") {
		t.Fatalf("want 25 test lines and no more line:\n%s", out)
	}
}

func TestSuiteSummary_AListOverTheCapEndsWithMore(t *testing.T) {
	t.Parallel()
	out, _ := runSummary(t, map[string]string{"go": failingTests(26)}, nil, "go")
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 26 || lines[25] != "go test: +1 more, see .ralph/go.log" || strings.Contains(strings.Join(lines[:25], "\n"), "more") {
		t.Fatalf("want 25 test lines then the more line:\n%s", out)
	}
}

func TestSuiteSummary_AFailureBeforeManyOkLinesIsStillNamed(t *testing.T) {
	t.Parallel()
	log := failingTests(3) + strings.Repeat("ok  \tgithub.com/x/y/pkg\t0.1s\n", 200)
	out, _ := runSummary(t, map[string]string{"go": log}, nil, "go")
	wantOut(t, out, "go test: Test1", "go test: Test2", "go test: Test3")
}

func TestSuiteSummary_TheCapIsPerSuiteAndPerKind(t *testing.T) {
	t.Parallel()
	out, _ := runSummary(t, map[string]string{"go": failingTests(30), "db": failingTests(30)}, nil, "go", "db")
	for _, s := range []string{"go", "db"} {
		if n := strings.Count(out, s+" test: Test"); n != 25 {
			t.Errorf("%s: %d test lines, want 25", s, n)
		}
		if !strings.Contains(out, s+" test: +5 more, see .ralph/"+s+".log\n") {
			t.Errorf("%s: no +5 more line:\n%s", s, out)
		}
	}
}

func TestSuiteSummary_AFailedSuiteWithNothingNamedPointsAtItsLog(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ suite, log string }{
		{"fmt", "gofmt: not formatted:\ninternal/x.go\n"},
		{"fmt", ""},
		{"go", "# pkg\nx.go:1:1: undefined: y\n"},
		{"unit", "ok  \tgithub.com/x/y\t1s\n Test Files  3 passed (3)\n"},
	} {
		out, _ := runSummary(t, map[string]string{c.suite: c.log}, nil, c.suite)
		wantOut(t, out, c.suite+noHit+c.suite+".log")
	}
}

func TestSuiteSummary_AnsiColourAndCRDoNotHideAFile(t *testing.T) {
	t.Parallel()
	log := fl + "\x1b[31m FAIL \x1b[39m  src/a.test.tsx > t\r\n" +
		fl + "\x1b[31m ❯ src/b.test.tsx (2 tests | 1 failed) 5ms\x1b[39m\r\n"
	wantOut(t, unitOut(t, log), "unit file: frontend/landing src/a.test.tsx", "unit file: frontend/landing src/b.test.tsx")
}

func TestSuiteSummary_OnlyTheNamedSuitesAreRead(t *testing.T) {
	t.Parallel()
	logs := map[string]string{"go": "FAIL\tgithub.com/x/bad\t1s\n", "unit": fl + " FAIL  src/a.test.ts > t\n"}
	out, _ := runSummary(t, logs, nil, "unit")
	if strings.Contains(out, "go ") || !strings.Contains(out, "unit file:") {
		t.Fatalf("want unit lines only:\n%s", out)
	}
}

func TestSuiteSummary_AMissingLogIsSkippedAndTheExitIsZero(t *testing.T) {
	t.Parallel()
	out, code := runSummary(t, map[string]string{"go": "FAIL\tgithub.com/x/bad\t1s\n"}, nil, "db")
	if out != "" || code != 0 {
		t.Fatalf("db without a log: out %q exit %d", out, code)
	}
	out, code = runSummary(t, map[string]string{"go": "FAIL\tgithub.com/x/bad\t1s\n"}, nil, "db", "go")
	wantOut(t, out, "go package: github.com/x/bad")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
}

func TestSuiteSummary_SameOutputUnderCAndUTF8Locales(t *testing.T) {
	t.Parallel()
	log := fl + " ❯ src/b.test.tsx (2 tests | 1 failed) 5ms\n" + fl + " FAIL  src/a.test.tsx > t\n" + ts + "ERROR" + ts + " @x/a@1.0.0 test: x\n"
	c, _ := runSummary(t, map[string]string{"unit": log}, []string{"LC_ALL=C"}, "unit")
	u, _ := runSummary(t, map[string]string{"unit": log}, []string{"LC_ALL=en_US.UTF-8"}, "unit")
	if c != u || !strings.Contains(c, "src/b.test.tsx") {
		t.Fatalf("C:\n%s\nUTF-8:\n%s", c, u)
	}
}
