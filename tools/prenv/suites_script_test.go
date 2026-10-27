// suites_script_test.go runs scripts/dev/suites.sh in a temp tree with shim make, go, pnpm and getconf.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// suitesRun is one run of suites.sh; zero values mean green and silent.
type suitesRun struct {
	port                  string // DEV_DB_PORT
	waitRC                int
	fmtOut, goOut, rOut   string // shim output; goOut is printed by `go test`
	fmtRC, goRC, rRC, eRC int    // make fmt-check, go test, pnpm -r test, pnpm e2e test:unit
	staleLogs             map[string]string
	cores                 string // getconf _NPROCESSORS_ONLN output; "" means 12
	noCores               bool   // getconf prints nothing
}

type suitesResult struct {
	stdout string
	code   int
	tree   string
}

const (
	makeShim = `echo "$*" >> "$SUITES_FX/make.argv"
if [ "$1" = fmt-check ]; then cat "$SUITES_FX/fmt.out"; exit "$FMT_RC"; fi
exit 0
`
	goShim = `echo "$*" >> "$SUITES_FX/go.argv"
if [ "$1" = test ]; then cat "$SUITES_FX/go.out"; exit "$GO_RC"; fi
exit 0
`
	getconfShim = `echo "$GETCONF_N"
`
	pnpmShim = `echo "$*" >> "$SUITES_FX/pnpm.argv"
case "$*" in
  "-r --no-bail"*) cat "$SUITES_FX/r.out"; exit "$R_RC" ;;
  *test:unit*) exit "$E_RC" ;;
esac
exit 0
`
)

func runSuites(t *testing.T, r suitesRun) suitesResult {
	t.Helper()
	root := repoRoot(t)
	tmp := t.TempDir()
	tree, fx, bin := filepath.Join(tmp, "tree"), filepath.Join(tmp, "fx"), filepath.Join(tmp, "bin")
	for _, d := range []string{filepath.Join(tree, "scripts/dev"), filepath.Join(tree, ".ralph"), fx, bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []string{"suites.sh", "suite-summary.sh"} {
		b, err := os.ReadFile(filepath.Join(root, "scripts/dev", s))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tree, "scripts/dev", s), b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeShim(t, filepath.Join(tree, "scripts/dev/wait-go-test-idle.sh"), "exit "+strconv.Itoa(r.waitRC)+"\n")
	for name, body := range map[string]string{"make": makeShim, "go": goShim, "pnpm": pnpmShim, "getconf": getconfShim} {
		writeShim(t, filepath.Join(bin, name), body)
	}
	for name, body := range map[string]string{"fmt.out": r.fmtOut, "go.out": r.goOut, "r.out": r.rOut} {
		if err := os.WriteFile(filepath.Join(fx, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range r.staleLogs {
		if err := os.WriteFile(filepath.Join(tree, ".ralph", name+".log"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cores := r.cores
	if cores == "" {
		cores = "12"
	}
	bash := "bash"
	if _, err := os.Stat("/bin/bash"); err == nil {
		bash = "/bin/bash"
	}
	cmd := exec.Command(bash, filepath.Join(tree, "scripts/dev/suites.sh"))
	cmd.Dir = tree
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"SUITES_FX="+fx,
		"DEV_DB_PORT="+r.port,
		"FMT_RC="+strconv.Itoa(r.fmtRC), "GO_RC="+strconv.Itoa(r.goRC),
		"R_RC="+strconv.Itoa(r.rRC), "E_RC="+strconv.Itoa(r.eRC))
	if r.noCores {
		cmd.Env = append(cmd.Env, "GETCONF_N=")
	} else {
		cmd.Env = append(cmd.Env, "GETCONF_N="+cores)
	}
	stdout, _, err := runToFiles(t, cmd, false)
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running suites.sh: %v", err)
		}
		code = ee.ExitCode()
	}
	return suitesResult{stdout: stdout, code: code, tree: tree}
}

func readFx(t *testing.T, res suitesResult, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(filepath.Dir(res.tree), "fx", name))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return string(b)
}

func TestSuites_TheE2EUnitTestsRunAfterTheFrontendPackagesFail(t *testing.T) {
	t.Parallel()
	res := runSuites(t, suitesRun{rRC: 1})
	calls := strings.Split(strings.TrimSpace(readFx(t, res, "pnpm.argv")), "\n")
	first, second := -1, -1
	for i, c := range calls {
		switch c {
		case "-r --no-bail --filter !@invoice-os/e2e test --maxWorkers=3":
			first = i
		case "--filter @invoice-os/e2e test:unit":
			second = i
		}
	}
	if first < 0 || second < first {
		t.Fatalf("pnpm calls %q: want the -r run, then the e2e unit run", calls)
	}
	if !strings.Contains(res.stdout, "unit=1\n") || res.code != 1 {
		t.Fatalf("code %d, stdout:\n%s", res.code, res.stdout)
	}
}

func rArgv(t *testing.T, res suitesResult) string {
	t.Helper()
	for _, c := range strings.Split(strings.TrimSpace(readFx(t, res, "pnpm.argv")), "\n") {
		if strings.HasPrefix(c, "-r --no-bail") {
			return c
		}
	}
	t.Fatalf("no pnpm -r call in %q", readFx(t, res, "pnpm.argv"))
	return ""
}

func TestSuites_VitestWorkersAreCoresOverFourMinOne(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ cores, workers string }{
		{"1", "1"}, {"3", "1"}, {"4", "1"}, {"7", "1"}, {"8", "2"}, {"12", "3"}, {"16", "4"},
	} {
		t.Run(c.cores, func(t *testing.T) {
			t.Parallel()
			res := runSuites(t, suitesRun{cores: c.cores})
			want := "-r --no-bail --filter !@invoice-os/e2e test --maxWorkers=" + c.workers
			if got := rArgv(t, res); got != want {
				t.Fatalf("cores %s: argv %q, want %q", c.cores, got, want)
			}
		})
	}
}

func TestSuites_UnusableCoreCountCapsAtOne(t *testing.T) {
	t.Parallel()
	for name, r := range map[string]suitesRun{
		"empty": {noCores: true}, "undefined": {cores: "undefined"}, "abc": {cores: "abc"}, "fraction": {cores: "4.5"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res := runSuites(t, r)
			want := "-r --no-bail --filter !@invoice-os/e2e test --maxWorkers=1"
			if got := rArgv(t, res); got != want {
				t.Fatalf("argv %q, want %q", got, want)
			}
			if !strings.Contains(res.stdout, "unit=0\n") || res.code != 0 {
				t.Fatalf("code %d, stdout:\n%s", res.code, res.stdout)
			}
		})
	}
}

func TestSuites_UnitFailsWhenOnlyTheE2EUnitTestsFail(t *testing.T) {
	t.Parallel()
	res := runSuites(t, suitesRun{eRC: 1})
	if !strings.Contains(res.stdout, "unit=1\n") || res.code != 1 {
		t.Fatalf("code %d, stdout:\n%s", res.code, res.stdout)
	}
}

func TestSuites_UnitPassesWhenBothPass(t *testing.T) {
	t.Parallel()
	res := runSuites(t, suitesRun{})
	if !strings.Contains(res.stdout, "unit=0\n") || res.code != 0 {
		t.Fatalf("code %d, stdout:\n%s", res.code, res.stdout)
	}
}

func TestSuites_NoReportSummaryFlag(t *testing.T) {
	t.Parallel()
	res := runSuites(t, suitesRun{rRC: 1})
	if strings.Contains(readFx(t, res, "pnpm.argv"), "--report-summary") {
		t.Fatal("a pnpm call carries --report-summary")
	}
	if _, err := os.Stat(filepath.Join(res.tree, "pnpm-exec-summary.json")); !os.IsNotExist(err) {
		t.Fatalf("pnpm-exec-summary.json exists in the tree (stat err %v)", err)
	}
}

func TestSuites_AGreenRunPrintsOneLinePerSuiteAndNothingElse(t *testing.T) {
	t.Parallel()
	res := runSuites(t, suitesRun{})
	wantOut(t, res.stdout, "fmt=0", "go=0", "db=skipped: no DEV_DB_PORT", "spa=0", "unit=0")
	if res.code != 0 {
		t.Fatalf("exit %d", res.code)
	}
}

func TestSuites_TheDBSuiteRunsWithTheWorkersPort(t *testing.T) {
	t.Parallel()
	res := runSuites(t, suitesRun{port: "5466"})
	wantOut(t, res.stdout, "fmt=0", "go=0", "db=0", "spa=0", "unit=0")
	if !strings.Contains(readFx(t, res, "make.argv"), "test-rls test-queue test-audit DEV_DB_PORT=5466\n") {
		t.Fatalf("make argv:\n%s", readFx(t, res, "make.argv"))
	}
}

func TestSuites_AWaitFailureStopsBeforeAnySuite(t *testing.T) {
	t.Parallel()
	res := runSuites(t, suitesRun{waitRC: 1})
	wantOut(t, res.stdout, "wait=1: a go test still runs in this worktree; no suite ran")
	if res.code != 1 {
		t.Fatalf("exit %d", res.code)
	}
	if _, err := os.Stat(filepath.Join(res.tree, ".ralph/fmt.log")); !os.IsNotExist(err) {
		t.Fatalf("fmt.log exists (stat err %v)", err)
	}
}

func TestSuites_EachSuiteKeepsItsLogName(t *testing.T) {
	t.Parallel()
	res := runSuites(t, suitesRun{port: "5466"})
	for _, n := range []string{"fmt", "go", "db", "spa", "unit", "wait"} {
		if _, err := os.Stat(filepath.Join(res.tree, ".ralph", n+".log")); err != nil {
			t.Errorf("%s.log: %v", n, err)
		}
	}
}

func TestSuites_ARedRunPrintsEverySuiteLineBeforeTheSummary(t *testing.T) {
	t.Parallel()
	res := runSuites(t, suitesRun{
		goOut: "FAIL\tgithub.com/x/bad\t1s\n", goRC: 1,
		rOut: fl + " FAIL  src/a.test.tsx > t\n", rRC: 1,
	})
	wantOut(t, res.stdout,
		"fmt=0", "go=1", "db=skipped: no DEV_DB_PORT", "spa=0", "unit=1",
		"go package: github.com/x/bad",
		"unit file: frontend/landing src/a.test.tsx")
	if res.code != 1 {
		t.Fatalf("exit %d", res.code)
	}
}

func TestSuites_AStaleLogOfASkippedSuiteIsNotSummarised(t *testing.T) {
	t.Parallel()
	res := runSuites(t, suitesRun{
		goOut: "FAIL\tgithub.com/x/bad\t1s\n", goRC: 1,
		staleLogs: map[string]string{"db": "FAIL\tgithub.com/x/old\t1s\n"},
	})
	for _, l := range strings.Split(res.stdout, "\n") {
		if strings.HasPrefix(l, "db ") || strings.HasPrefix(l, "db:") {
			t.Fatalf("db summarised: %q", l)
		}
	}
	if !strings.Contains(res.stdout, "go package: github.com/x/bad\n") {
		t.Fatalf("go not summarised:\n%s", res.stdout)
	}
}

func TestSuites_AFailureBeforeManyOkLinesIsStillNamed(t *testing.T) {
	t.Parallel()
	out := "--- FAIL: TestA (0.1s)\n--- FAIL: TestB (0.1s)\n--- FAIL: TestC (0.1s)\n"
	for i := 0; i < 100; i++ {
		out += "ok  \tgithub.com/x/p" + strconv.Itoa(i) + "\t1s\n"
	}
	res := runSuites(t, suitesRun{goOut: out, goRC: 1})
	for _, n := range []string{"TestA", "TestB", "TestC"} {
		if !strings.Contains(res.stdout, "go test: "+n+"\n") {
			t.Errorf("%s not named:\n%s", n, res.stdout)
		}
	}
}

func TestSuites_ASuiteThatFailsWithNothingNamedPointsAtItsLog(t *testing.T) {
	t.Parallel()
	res := runSuites(t, suitesRun{fmtOut: "gofmt: not formatted:\ninternal/x.go\n", fmtRC: 1})
	wantOut(t, res.stdout,
		"fmt=1", "go=0", "db=skipped: no DEV_DB_PORT", "spa=0", "unit=0",
		"fmt: no failing file, package or test named, see .ralph/fmt.log")
	if res.code != 1 {
		t.Fatalf("exit %d", res.code)
	}
}
