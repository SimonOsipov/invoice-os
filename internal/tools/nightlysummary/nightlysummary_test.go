package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const (
	pkgX     = modulePrefix + "/internal/x"
	workflow = ".github/workflows/ci.yml"
)

// ---- Go stream builders ----

type ge struct {
	Action      string
	Package     string `json:",omitempty"`
	Test        string `json:",omitempty"`
	Output      string `json:",omitempty"`
	ImportPath  string `json:",omitempty"`
	FailedBuild string `json:",omitempty"`
}

func stream(parts ...[]ge) string {
	var b strings.Builder
	for _, p := range parts {
		for _, e := range p {
			raw, _ := json.Marshal(e)
			b.Write(raw)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// tst is a test that ran, printed output and ended with result ("" = never ended).
func tst(pkg, name, result string, out ...string) []ge {
	es := []ge{{Action: "run", Package: pkg, Test: name}}
	for _, o := range out {
		es = append(es, ge{Action: "output", Package: pkg, Test: name, Output: o})
	}
	if result != "" {
		es = append(es, ge{Action: result, Package: pkg, Test: name})
	}
	return es
}

func pkgEnd(pkg, result string, out ...string) []ge {
	es := []ge{{Action: "start", Package: pkg}}
	for _, o := range out {
		es = append(es, ge{Action: "output", Package: pkg, Output: o})
	}
	return append(es, ge{Action: result, Package: pkg})
}

// attemptOf wraps tests with the package start and end of one go test run.
func attemptOf(pkg, result string, tests ...[]ge) []ge {
	es := []ge{{Action: "start", Package: pkg}}
	for _, t := range tests {
		es = append(es, t...)
	}
	return append(es, ge{Action: result, Package: pkg})
}

type judged struct{ passed, fails, flakes []outcome }

func judgeStreams(t *testing.T, streams ...string) judged {
	t.Helper()
	var pfs []parsedFile
	for i, s := range streams {
		pf, err := parseGo(strings.NewReader(s), fmt.Sprintf("gotest-%d.json", i))
		if err != nil {
			t.Fatal(err)
		}
		pfs = append(pfs, pf)
	}
	return split(judgeFiles(pfs))
}

func split(outs []outcome) judged {
	var j judged
	for _, o := range outs {
		switch o.status {
		case statusPass:
			j.passed = append(j.passed, o)
		case statusFail:
			j.fails = append(j.fails, o)
		case statusFlake:
			j.flakes = append(j.flakes, o)
		}
	}
	return j
}

func tests(os []outcome) []string {
	out := []string{}
	for _, o := range os {
		out = append(out, o.test)
	}
	return out
}

func fixture(t *testing.T, parts ...string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{"testdata"}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func eq(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %#v\nwant %#v", got, want)
	}
}

// ---- Go JSON ----

func TestGoJSON_FailedSubtestListedParentNot(t *testing.T) {
	j := judgeStreams(t, string(fixture(t, "real", "gotest-basic.json")))
	eq(t, tests(j.fails), []string{"TestBad/sub"})
	eq(t, j.fails[0].msg, "a_test.go:7: want 1, got 2")
	eq(t, tests(j.passed), []string{"TestOK", "TestBad/fine"})
}

func TestGoJSON_ParentFailingAloneIsListed(t *testing.T) {
	s := stream(attemptOf(pkgX, "fail",
		tst(pkgX, "TestP", "fail", "=== RUN   TestP\n", "    p_test.go:9: cleanup failed\n", "--- FAIL: TestP (0.00s)\n"),
		tst(pkgX, "TestP/a", "pass"), tst(pkgX, "TestP/b", "pass")))
	j := judgeStreams(t, s)
	eq(t, tests(j.fails), []string{"TestP"})
	eq(t, j.fails[0].msg, "p_test.go:9: cleanup failed")
	eq(t, len(j.passed), 2)
}

func TestGoJSON_BuildFailureIsOnePackageEntry(t *testing.T) {
	j := judgeStreams(t, string(fixture(t, "real", "gotest-broken.json")))
	eq(t, tests(j.fails), []string{"example.com/gj/broken (package)"})
	eq(t, j.fails[0].msg, "broken/b.go:3:9: syntax error: unexpected {, expected )")
	eq(t, len(j.passed)+len(j.flakes), 0)
}

func TestGoJSON_PackageFailWithFailedTestAddsNoPackageEntry(t *testing.T) {
	j := judgeStreams(t, stream(attemptOf(pkgX, "fail", tst(pkgX, "TestBad", "fail", "    b_test.go:1: no\n"))))
	eq(t, tests(j.fails), []string{"TestBad"})
}

func TestGoJSON_FileIsPackageDirWithoutModule(t *testing.T) {
	root := modulePrefix
	tenancy := modulePrefix + "/internal/tenancy"
	j := judgeStreams(t, stream(
		attemptOf(tenancy, "fail", tst(tenancy, "TestA", "fail", "    a: x\n")),
		attemptOf(root, "fail", tst(root, "TestB", "fail", "    b: x\n"))))
	files := map[string]string{}
	for _, o := range j.fails {
		files[o.test] = o.file
	}
	eq(t, files, map[string]string{"TestA": "internal/tenancy", "TestB": "."})
}

func TestGoJSON_MessageSkipsRunAndResultLines(t *testing.T) {
	j := judgeStreams(t, stream(tst(pkgX, "TestX", "fail", "=== RUN   TestX\n", "    x_test.go:4: want 1\n", "--- FAIL: TestX\n")))
	eq(t, j.fails[0].msg, "x_test.go:4: want 1")

	j = judgeStreams(t, stream(tst(pkgX, "TestX", "fail", "--- FAIL: TestX\n", "    x_test.go:4: want 1\n")))
	eq(t, j.fails[0].msg, "x_test.go:4: want 1")
}

func TestGoJSON_MessageCappedAt300(t *testing.T) {
	j := judgeStreams(t, stream(tst(pkgX, "TestX", "fail", strings.Repeat("a", 1000)+"\n")))
	eq(t, len([]rune(j.fails[0].msg)), 300)
}

func TestGoJSON_TimeoutThenWholeRerunPass(t *testing.T) {
	j := judgeStreams(t, string(fixture(t, "real", "gotest-timeout-rerun.json")))
	eq(t, tests(j.flakes), []string{"TestSlow"})
	eq(t, j.flakes[0].msg, "panic: test timed out after 1s")
	eq(t, len(j.passed), 2)
	eq(t, len(j.fails), 0)
}

func TestGoJSON_UnfinishedTestOnlyAttemptIsFailure(t *testing.T) {
	j := judgeStreams(t, string(fixture(t, "real", "gotest-timeout.json")))
	eq(t, tests(j.fails), []string{"TestSlow"})
	eq(t, j.fails[0].msg, "panic: test timed out after 1s")
	eq(t, len(j.flakes), 0)

	e := newEnv(t)
	e.write("go", "gotest-go.json", string(fixture(t, "real", "gotest-timeout.json")))
	r := e.run()
	eq(t, r.code, 1)
	eq(t, r.dec.Verdict, "red")
}

func TestGoJSON_PausedParallelTestsAreNotFlakesAfterAPanicElsewhere(t *testing.T) {
	j := judgeStreams(t, string(fixture(t, "real", "gotest-parallel-panic.json"))+string(fixture(t, "real", "gotest-parallel-panic-rerun.json")))
	eq(t, tests(j.flakes), []string{"TestSeq"})
	eq(t, tests(j.passed), []string{"TestP1", "TestP2", "TestP3"})
	eq(t, len(j.fails), 0)
}

func TestGoJSON_PausedTestWithNoResultIsNotAFailedAttempt(t *testing.T) {
	paused := []ge{{Action: "run", Package: pkgX, Test: "TestP"}, {Action: "pause", Package: pkgX, Test: "TestP"}}
	j := judgeStreams(t, stream(attemptOf(pkgX, "fail", paused)))
	for _, o := range j.fails {
		if o.test == "TestP" {
			t.Fatalf("paused TestP counted as failed: %+v", o)
		}
	}

	resumed := append(paused, ge{Action: "cont", Package: pkgX, Test: "TestP"})
	j = judgeStreams(t, stream(attemptOf(pkgX, "fail", resumed)))
	eq(t, tests(j.fails), []string{"TestP"})
}

func TestGoJSON_PanicThenWholeRerunPass(t *testing.T) {
	j := judgeStreams(t, string(fixture(t, "real", "gotest-panic-rerun.json")))
	eq(t, tests(j.flakes), []string{"TestB"})
	eq(t, j.flakes[0].msg, "panic: boom [recovered, repanicked]")
	eq(t, tests(j.passed), []string{"TestA", "TestC"})
	eq(t, len(j.fails), 0)
}

func TestGoJSON_PackageFailThenPassIsPackageFlake(t *testing.T) {
	s := stream(pkgEnd(pkgX, "fail", "FAIL\n"), attemptOf(pkgX, "pass", tst(pkgX, "TestM", "pass")))
	j := judgeStreams(t, s)
	eq(t, tests(j.flakes), []string{pkgX + " (package)"})
	eq(t, len(j.passed), 1)
	eq(t, len(j.fails), 0)
}

func TestGoJSON_PackageFailTwiceIsPackageFailure(t *testing.T) {
	j := judgeStreams(t, stream(pkgEnd(pkgX, "fail", "FAIL\n"), pkgEnd(pkgX, "fail", "FAIL\n")))
	eq(t, tests(j.fails), []string{pkgX + " (package)"})
	eq(t, j.fails[0].msg, "FAIL")

	e := newEnv(t)
	e.write("go", "gotest-go.json", stream(pkgEnd(pkgX, "fail", "FAIL\n"), pkgEnd(pkgX, "fail", "FAIL\n")))
	eq(t, e.run().code, 1)
}

// ---- vitest ----

func vitest(file, fileStatus, fileMsg string, asserts ...string) string {
	return fmt.Sprintf(`{"testResults":[{"name":%q,"status":%q,"message":%q,"assertionResults":[%s]}]}`,
		file, fileStatus, fileMsg, strings.Join(asserts, ","))
}

func va(name, status string, msgs ...string) string {
	raw, _ := json.Marshal(msgs)
	if msgs == nil {
		raw = []byte("[]")
	}
	return fmt.Sprintf(`{"fullName":%q,"title":%q,"status":%q,"failureMessages":%s}`, name, name, status, raw)
}

func judgeVitest(t *testing.T, data, workspace string) judged {
	t.Helper()
	pf, err := parseVitest([]byte(data), workspace)
	if err != nil {
		t.Fatal(err)
	}
	return split(judgeFiles([]parsedFile{pf}))
}

func TestVitest_FlakeFailureAndPassInOneFile(t *testing.T) {
	j := judgeVitest(t, string(fixture(t, "real", "vitest-flake.json")), "")
	eq(t, tests(j.flakes), []string{"flips"})
	eq(t, tests(j.fails), []string{"always fails"})
	eq(t, j.fails[0].msg, "AssertionError: expected 1 to be 2 // Object.is equality")
	eq(t, len(j.passed), 1)

	m := judgeVitest(t, vitest("/w/a.test.ts", "failed", "", va("x", "failed", "first line\nsecond line", "other")), "")
	eq(t, m.fails[0].msg, "first line")
}

func TestVitest_SkippedPendingTodoNotCounted(t *testing.T) {
	d := vitest("/w/a.test.ts", "passed", "", va("s", "skipped"), va("p", "pending"), va("t", "todo"), va("ok", "passed"))
	j := judgeVitest(t, d, "")
	eq(t, len(j.passed), 1)
	eq(t, len(j.fails)+len(j.flakes), 0)
}

func TestVitest_FileLevelFailure(t *testing.T) {
	j := judgeVitest(t, vitest("/w/a.test.ts", "failed", "Error: Cannot find module './x'\n    at y"), "")
	eq(t, tests(j.fails), []string{"(file)"})
	eq(t, j.fails[0].msg, "Error: Cannot find module './x'")
}

func TestVitest_FileStripsWorkspace(t *testing.T) {
	d := vitest("/home/runner/work/invoice-os/invoice-os/frontend/app/src/a.test.ts", "failed", "", va("x", "failed", "boom"))
	eq(t, judgeVitest(t, d, "/home/runner/work/invoice-os/invoice-os").fails[0].file, "frontend/app/src/a.test.ts")
	eq(t, judgeVitest(t, d, "/other/place").fails[0].file, "/home/runner/work/invoice-os/invoice-os/frontend/app/src/a.test.ts")
	eq(t, judgeVitest(t, d, "").fails[0].file, "/home/runner/work/invoice-os/invoice-os/frontend/app/src/a.test.ts")
}

func TestVitest_DuplicateFullNameJudgedOneByOne(t *testing.T) {
	j := judgeVitest(t, vitest("/w/a.test.ts", "failed", "", va("adds", "failed", "no"), va("adds", "passed")), "")
	eq(t, len(j.fails), 1)
	eq(t, len(j.passed), 1)
	eq(t, len(j.flakes), 0)
}

// ---- JUnit ----

func jcase(cls, name, inner string) string {
	if inner == "" {
		return fmt.Sprintf(`<testcase classname=%q name=%q file="tests/test_x.py" />`, cls, name)
	}
	return fmt.Sprintf(`<testcase classname=%q name=%q file="tests/test_x.py">%s</testcase>`, cls, name, inner)
}

func junit(suite string, cases ...string) string {
	return fmt.Sprintf(`<testsuites><testsuite name=%q>%s</testsuite></testsuites>`, suite, strings.Join(cases, ""))
}

func judgeJUnit(t *testing.T, files map[string]string) judged {
	t.Helper()
	var names []string
	for n := range files {
		names = append(names, n)
	}
	fileOrder(names)
	var pfs []parsedFile
	for _, n := range names {
		pf, err := parseJUnit([]byte(files[n]), junitSet(n))
		if err != nil {
			t.Fatal(err)
		}
		pfs = append(pfs, pf)
	}
	return split(judgeFiles(pfs))
}

func TestJUnit_FailureErrorSkippedPass(t *testing.T) {
	real := string(fixture(t, "real", "junit-docling.xml"))
	j := judgeJUnit(t, map[string]string{"junit-a.xml": real})
	eq(t, tests(j.fails), []string{"tests.test_x::test_bad"})
	eq(t, j.fails[0].msg, "AssertionError: one is not two")
	eq(t, len(j.passed), 2)

	extra := junit("sidecar/docling",
		jcase("tests.test_x", "test_ok", ""),
		jcase("tests.test_x", "test_err", `<error message="RuntimeError: kaboom">trace</error>`),
		jcase("tests.test_x", "test_skip", `<skipped message="no db" />`),
		jcase("tests.test_x", "test_pass", ""))
	j = judgeJUnit(t, map[string]string{"junit-b.xml": extra})
	eq(t, tests(j.fails), []string{"tests.test_x::test_err"})
	eq(t, j.fails[0].msg, "RuntimeError: kaboom")
	eq(t, len(j.passed), 2)
}

func TestJUnit_FileJoinsSuiteName(t *testing.T) {
	j := judgeJUnit(t, map[string]string{"junit-a.xml": junit("sidecar/docling", jcase("c", "t", `<failure message="m"/>`))})
	eq(t, j.fails[0].file, "sidecar/docling/tests/test_x.py")
	j = judgeJUnit(t, map[string]string{"junit-a.xml": junit("pytest", jcase("c", "t", `<failure message="m"/>`))})
	eq(t, j.fails[0].file, "tests/test_x.py")
}

func TestJUnit_TestsuiteRootAccepted(t *testing.T) {
	d := `<testsuite name="pytest"><testcase classname="c" name="t" file="f.py"><failure message="m"/></testcase></testsuite>`
	j := judgeJUnit(t, map[string]string{"junit-a.xml": d})
	eq(t, len(j.fails), 1)
}

func TestJUnit_MessageFromTextWhenNoAttribute(t *testing.T) {
	j := judgeJUnit(t, map[string]string{"junit-a.xml": junit("pytest", jcase("c", "t", "<failure>line one\nline two</failure>"))})
	eq(t, j.fails[0].msg, "line one")
}

// ---- attempts ----

func TestAttempts_FailThenPassIsFlake(t *testing.T) {
	s := stream(
		tst(pkgX, "TestX", "fail", "    x: bad\n"), tst(pkgX, "TestY", "fail", "    y: bad\n"), tst(pkgX, "TestZ", "pass"),
		tst(pkgX, "TestX", "pass"), tst(pkgX, "TestY", "fail", "    y: still bad\n"))
	j := judgeStreams(t, s)
	eq(t, tests(j.flakes), []string{"TestX"})
	eq(t, tests(j.fails), []string{"TestY"})
	eq(t, j.fails[0].msg, "y: still bad")
	eq(t, len(j.passed), 1)
}

func TestAttempts_FlakySubtestHidesItsParent(t *testing.T) {
	s := stream(
		tst(pkgX, "TestB/sub", "fail", "    s: bad\n"), tst(pkgX, "TestB", "fail"),
		tst(pkgX, "TestB/sub", "pass"), tst(pkgX, "TestB", "pass"))
	j := judgeStreams(t, s)
	eq(t, tests(j.flakes), []string{"TestB/sub"})
	eq(t, len(j.fails), 0)
}

func TestAttempts_AcrossGoFilesIsNotAFlake(t *testing.T) {
	a := stream(tst(pkgX, "TestX", "fail", "    x: bad\n"))
	b := stream(tst(pkgX, "TestX", "pass"))
	j := judgeStreams(t, a, b)
	eq(t, tests(j.fails), []string{"TestX"})
	eq(t, len(j.passed), 1)
	eq(t, len(j.flakes), 0)

	e := newEnv(t)
	e.write("go", "gotest-a.json", a)
	e.write("go", "gotest-b.json", b)
	eq(t, e.run().code, 1)
}

func TestAttempts_JUnitRetryPairIsFlake(t *testing.T) {
	first := junit("pytest", jcase("tests.test_x", "test_bad", `<failure message="no"/>`), jcase("tests.test_x", "test_ok", ""))
	retry := junit("pytest", jcase("tests.test_x", "test_bad", ""))
	j := judgeJUnit(t, map[string]string{"junit-docling.xml": first, "junit-docling-retry.xml": retry})
	eq(t, tests(j.flakes), []string{"tests.test_x::test_bad"})
	eq(t, len(j.fails), 0)
	eq(t, len(j.passed), 1)

	real := judgeJUnit(t, map[string]string{
		"junit-docling.xml":       string(fixture(t, "real", "junit-docling.xml")),
		"junit-docling-retry.xml": string(fixture(t, "real", "junit-pass-retry.xml")),
	})
	eq(t, tests(real.flakes), []string{"tests.test_x::test_bad"})
}

func TestAttempts_JUnitFailsTwiceIsFailure(t *testing.T) {
	first := junit("pytest", jcase("tests.test_x", "test_bad", `<failure message="first"/>`))
	retry := junit("pytest", jcase("tests.test_x", "test_bad", `<failure message="retry"/>`))
	j := judgeJUnit(t, map[string]string{"junit-docling.xml": first, "junit-docling-retry.xml": retry})
	eq(t, tests(j.fails), []string{"tests.test_x::test_bad"})
	eq(t, j.fails[0].msg, "retry")
	eq(t, len(j.flakes), 0)
}

func TestAttempts_JUnitUnpairedFilesNotGrouped(t *testing.T) {
	a := junit("pytest", jcase("tests.test_x", "test_bad", `<failure message="no"/>`))
	b := junit("pytest", jcase("tests.test_x", "test_bad", ""))
	j := judgeJUnit(t, map[string]string{"junit-a.xml": a, "junit-b.xml": b})
	eq(t, len(j.fails), 1)
	eq(t, len(j.passed), 1)
	eq(t, len(j.flakes), 0)
}

// ---- whole run ----

type decoded struct {
	Verdict  string        `json:"verdict"`
	Suites   []suiteResult `json:"suites"`
	Failures []failure     `json:"failures"`
	Flakes   []flake       `json:"flakes"`
}

type runResult struct {
	code           int
	stdout, stderr string
	raw            []byte
	dec            decoded
	obj            map[string]json.RawMessage
	scenarios      []scenario
}

type env struct {
	t                              *testing.T
	dir, suites, jobsPath, results string
	out                            string
	jobs                           []job
	e2eFlag                        []string
}

const defaultSuites = "go=Go=results\nfrontend=Frontend=results\ndocling-test=Docling image (test suite)=results\ndocker-canary=Docker image (canary)\n"

func newEnv(t *testing.T) *env {
	dir := t.TempDir()
	e := &env{t: t, dir: dir, suites: filepath.Join(dir, "suites.txt"), jobsPath: filepath.Join(dir, "jobs.json"),
		results: filepath.Join(dir, "results"), out: filepath.Join(dir, "summary.json")}
	e.writeFile(e.suites, defaultSuites)
	for _, n := range []string{"Go", "Frontend", "Docling image (test suite)", "Docker image (canary)"} {
		e.jobs = append(e.jobs, job{Name: n, Conclusion: "success"})
	}
	e.write("go", "gotest-go.json", stream(attemptOf(pkgX, "pass", tst(pkgX, "TestA", "pass"))))
	e.write("frontend", "vitest-app.json", vitest("/w/a.test.ts", "passed", "", va("ok", "passed")))
	e.write("docling-test", "junit-docling.xml", junit("pytest", jcase("c", "t", "")))
	return e
}

func (e *env) writeFile(path, content string) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) write(key, name, content string) {
	e.writeFile(filepath.Join(e.results, "results-"+key, name), content)
}

func (e *env) clear(key string) {
	if err := os.RemoveAll(filepath.Join(e.results, "results-"+key)); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) job(name string) *job {
	for i := range e.jobs {
		if e.jobs[i].Name == name {
			return &e.jobs[i]
		}
	}
	e.t.Fatalf("no job %q", name)
	return nil
}

func (e *env) run(extra ...string) runResult {
	e.t.Helper()
	raw, err := json.Marshal(e.jobs)
	if err != nil {
		e.t.Fatal(err)
	}
	e.writeFile(e.jobsPath, string(raw))
	return runArgs(e.t, e.out, append([]string{
		"-suites", e.suites, "-jobs", e.jobsPath, "-results", e.results, "-out", e.out,
		"-now", "2026-10-29T02:00:00Z", "-started", "2026-10-29T01:17:00Z", "-run-id", "1", "-sha", "abc",
		"-workspace", "/home/runner/work/invoice-os/invoice-os",
	}, extra...))
}

func runArgs(t *testing.T, out string, args []string) runResult {
	t.Helper()
	var so, se bytes.Buffer
	r := runResult{code: run(args, &so, &se), stdout: so.String(), stderr: se.String()}
	raw, err := os.ReadFile(out)
	if err != nil {
		return r
	}
	r.raw = raw
	if err := json.Unmarshal(raw, &r.dec); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &r.obj); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(r.obj["scenarios"], &r.scenarios); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r runResult) suite(name string) *suiteResult {
	for i := range r.dec.Suites {
		if r.dec.Suites[i].Name == name {
			return &r.dec.Suites[i]
		}
	}
	return nil
}

func (r runResult) failureTests(suite string) []string {
	out := []string{}
	for _, f := range r.dec.Failures {
		if f.Suite == suite {
			out = append(out, f.Test)
		}
	}
	return out
}

// assertScenariosMatchCounts checks each E2E suite's scenarios against its counts.
// Failed is counted from failures[] minus the job, step and results-file entries, so a dropped failed scenario shows.
func assertScenariosMatchCounts(t *testing.T, r runResult, e2eKeys ...string) {
	t.Helper()
	isE2E := map[string]bool{}
	for _, k := range e2eKeys {
		isE2E[k] = true
	}
	got := map[string]map[string]int{}
	for _, sc := range r.scenarios {
		if !isE2E[sc.Suite] {
			t.Fatalf("scenario of non-E2E suite: %+v", sc)
		}
		if got[sc.Suite] == nil {
			got[sc.Suite] = map[string]int{}
		}
		got[sc.Suite][sc.Status]++
	}
	notTest := func(f failure) bool {
		switch f.Test {
		case "suite did not run", "no test results", "results unreadable", "results empty", "(job)":
			return true
		}
		return f.Message == "step failure" || f.Message == "not run: an earlier step failed"
	}
	for _, k := range e2eKeys {
		sr := r.suite(k)
		if sr == nil {
			t.Fatalf("no suite %q", k)
		}
		testFails := 0
		for _, f := range r.dec.Failures {
			if f.Suite == k && !notTest(f) {
				testFails++
			}
		}
		eq(t, got[k]["passed"], sr.Passed)
		eq(t, got[k]["flaky"], sr.Flaky)
		eq(t, got[k]["failed"], testFails)
	}
}

func TestRun_OutputHasSchemaTwoKeys(t *testing.T) {
	r := newEnv(t).run()
	keys := []string{}
	for k := range r.obj {
		keys = append(keys, k)
	}
	want := []string{"schema", "run_id", "sha", "started_at", "finished_at", "verdict", "suites", "failures", "flakes", "scenarios"}
	eq(t, len(keys), len(want))
	for _, k := range want {
		if _, ok := r.obj[k]; !ok {
			t.Fatalf("missing key %q in %v", k, keys)
		}
	}
	eq(t, string(r.obj["schema"]), "2")
	eq(t, string(r.obj["run_id"]), "1")
	eq(t, string(r.obj["sha"]), `"abc"`)
	eq(t, string(r.obj["finished_at"]), `"2026-10-29T02:00:00Z"`)
	eq(t, string(r.obj["started_at"]), `"2026-10-29T01:17:00Z"`)
}

func TestRun_EmptyListsAreArraysNotNull(t *testing.T) {
	r := newEnv(t).run()
	for _, want := range []string{`"failures":[]`, `"flakes":[]`, `"scenarios":[]`} {
		if !strings.Contains(string(r.raw), want) {
			t.Fatalf("%s missing in %s", want, r.raw)
		}
	}
	if strings.Contains(string(r.raw), "null") {
		t.Fatalf("null in %s", r.raw)
	}
	e := newEnv(t)
	e.suites = filepath.Join(e.dir, "s2.txt")
	e.writeFile(e.suites, "docker-canary=Docker image (canary)\n")
	e.job("Docker image (canary)").Conclusion = "success"
	r = e.run()
	eq(t, len(r.dec.Suites), 1)
}

func TestRun_SchemaOneReaderReadsSchemaTwo(t *testing.T) {
	r := newEnv(t).run()
	var old decoded
	if err := json.Unmarshal(r.raw, &old); err != nil {
		t.Fatal(err)
	}
	eq(t, old.Verdict, "green")
	eq(t, old.Suites, []suiteResult{{"go", 1, 0, 0}, {"frontend", 1, 0, 0}, {"docling-test", 1, 0, 0}, {"docker-canary", 1, 0, 0}})
	eq(t, len(old.Failures), 0)
	eq(t, len(old.Flakes), 0)
}

func TestRun_SectionNamedScenariosIsUsageError(t *testing.T) {
	e := newEnv(t)
	sec := filepath.Join(e.dir, "x.json")
	e.writeFile(sec, `{"ok":true}`)
	r := e.run("-section", "scenarios="+sec)
	eq(t, r.code, 2)
	eq(t, r.raw == nil, true)
}

func TestRun_FlakeAloneKeepsVerdictGreen(t *testing.T) {
	r := runFixture(t, "flake")
	eq(t, r.code, 0)
	eq(t, r.dec.Verdict, "green")
	eq(t, len(r.dec.Flakes), 2)
	eq(t, r.suite("frontend").Flaky, 1)
	eq(t, r.suite("docling-test").Flaky, 1)
}

func TestRun_SkippedJobIsAbsentAndRed(t *testing.T) {
	for _, conc := range []string{"skipped", "cancelled"} {
		e := newEnv(t)
		e.job("Docling image (test suite)").Conclusion = conc
		r := e.run()
		eq(t, r.code, 1)
		eq(t, r.suite("docling-test") == nil, true)
		eq(t, r.dec.Failures, []failure{{"docling-test", "suite did not run", workflow, "job Docling image (test suite) " + conc}})
	}
}

func TestRun_MissingJobIsAbsentAndRedEvenWithResults(t *testing.T) {
	e := newEnv(t)
	e.jobs = []job{*e.job("Go"), *e.job("Frontend"), *e.job("Docker image (canary)")}
	r := e.run()
	eq(t, r.code, 1)
	eq(t, r.suite("docling-test") == nil, true)
	eq(t, r.dec.Failures[0].Message, "job Docling image (test suite) missing")
}

func TestRun_JobLevelSuccessCountsOnePass(t *testing.T) {
	r := newEnv(t).run()
	eq(t, *r.suite("docker-canary"), suiteResult{"docker-canary", 1, 0, 0})
}

func steps(pairs ...string) []step {
	var s []step
	for i := 0; i+1 < len(pairs); i += 2 {
		s = append(s, step{Name: pairs[i], Conclusion: pairs[i+1], Number: i/2 + 1})
	}
	return s
}

func TestRun_JobLevelFailureNamesFailedSteps(t *testing.T) {
	e := newEnv(t)
	j := e.job("Docker image (canary)")
	j.Conclusion = "failure"
	j.Steps = steps("Set up job", "success", "Build one plain service image (SERVICE=tenancy)", "failure", "Copy x", "skipped")
	r := e.run()
	eq(t, r.code, 1)
	eq(t, r.dec.Failures[0], failure{"docker-canary", "Build one plain service image (SERVICE=tenancy)", workflow, "step failure"})
	eq(t, r.failureTests("docker-canary")[1], "Copy x")
}

func TestRun_FailedJobWithGreenResultsNamesFailedStep(t *testing.T) {
	e := newEnv(t)
	j := e.job("Go")
	j.Conclusion = "failure"
	j.Steps = steps("Test", "success", "JWKS failure budget under the race detector", "failure", "Test the mock-issuer build (-tags mockissuer)", "skipped")
	r := e.run()
	eq(t, r.dec.Failures, []failure{
		{"go", "JWKS failure budget under the race detector", workflow, "step failure"},
		{"go", "Test the mock-issuer build (-tags mockissuer)", workflow, "not run: an earlier step failed"},
	})
	eq(t, r.suite("go").Passed, 1)
}

func TestRun_FailedJobWithNoFailedStepIsOneJobEntry(t *testing.T) {
	e := newEnv(t)
	j := e.job("Docker image (canary)")
	j.Conclusion = "failure"
	j.Steps = steps("a", "success", "b", "success")
	eq(t, e.run().dec.Failures, []failure{{"docker-canary", "(job)", workflow, "job failure"}})
}

func TestRun_TimedOutJobCountsAsRan(t *testing.T) {
	e := newEnv(t)
	e.job("Docker image (canary)").Conclusion = "timed_out"
	r := e.run()
	eq(t, r.suite("docker-canary") != nil, true)
	eq(t, r.dec.Failures, []failure{{"docker-canary", "(job)", workflow, "job timed_out"}})
}

func TestRun_SkippedStepsAfterFailureAreListed(t *testing.T) {
	e := newEnv(t)
	j := e.job("Go")
	j.Conclusion = "failure"
	j.Steps = []step{
		{"Vet", "success", 7}, {"Test", "failure", 8},
		{"JWKS failure budget under the race detector", "skipped", 9},
		{"Test the mock-issuer build (-tags mockissuer)", "skipped", 10},
		{"Post Run actions/setup-go@v5", "skipped", 21}, {"Upload the nightly results", "success", 22},
	}
	e.write("go", "gotest-go.json", stream(attemptOf(pkgX, "fail", tst(pkgX, "TestBad", "fail", "    b: x\n"))))
	r := e.run()
	got := map[string]string{}
	for _, f := range r.dec.Failures {
		got[f.Test] = f.Message
	}
	eq(t, got, map[string]string{
		"TestBad": "b: x",
		"JWKS failure budget under the race detector":   "not run: an earlier step failed",
		"Test the mock-issuer build (-tags mockissuer)": "not run: an earlier step failed",
	})

	j.Steps = []step{{"Late skipped", "skipped", 3}, {"Fails", "failure", 2}, {"Early skipped", "skipped", 1}}
	e.write("go", "gotest-go.json", stream(attemptOf(pkgX, "pass", tst(pkgX, "TestA", "pass"))))
	eq(t, e.run().failureTests("go"), []string{"Fails", "Late skipped"})
}

func TestRun_SkippedStepBeforeFailureNotListed(t *testing.T) {
	e := newEnv(t)
	j := e.job("Docker image (canary)")
	j.Conclusion = "failure"
	j.Steps = steps("one", "success", "two", "success", "three", "skipped", "four", "success", "five", "failure")
	eq(t, e.run().failureTests("docker-canary"), []string{"five"})
}

func TestRun_SuccessfulJobSkippedStepsNotListed(t *testing.T) {
	e := newEnv(t)
	e.job("Docker image (canary)").Steps = steps("a", "success", "b", "skipped")
	r := e.run()
	eq(t, r.code, 0)
	eq(t, len(r.dec.Failures), 0)
}

func TestRun_ResultsSuiteWithoutFilesIsRed(t *testing.T) {
	e := newEnv(t)
	e.clear("go")
	r := e.run()
	eq(t, r.code, 1)
	eq(t, r.dec.Failures, []failure{{"go", "no test results", workflow, "job Go wrote no results"}})
}

func TestRun_UnmarkedSuiteWithoutFilesIsJobLevel(t *testing.T) {
	r := newEnv(t).run()
	eq(t, r.code, 0)
	eq(t, r.suite("docker-canary").Passed, 1)
}

func TestRun_EmptyResultFilesAreRed(t *testing.T) {
	e := newEnv(t)
	e.clear("go")
	e.write("go", "gotest-x.json", stream([]ge{{Action: "start", Package: pkgX}}))
	e.clear("frontend")
	e.write("frontend", "vitest-app.json", `{"testResults":[]}`)
	e.clear("docling-test")
	e.write("docling-test", "junit-docling.xml", `<testsuites><testsuite name="pytest"/></testsuites>`)
	r := e.run()
	eq(t, r.code, 1)
	got := map[string]string{}
	for _, f := range r.dec.Failures {
		got[f.Suite+"/"+f.Test] = f.File
	}
	eq(t, got, map[string]string{
		"go/results empty": "gotest-x.json", "go/no test results": workflow,
		"frontend/results empty": "vitest-app.json", "frontend/no test results": workflow,
		"docling-test/results empty": "junit-docling.xml", "docling-test/no test results": workflow,
	})
}

func TestRun_OneEmptyFileBesideAFullOne(t *testing.T) {
	e := newEnv(t)
	e.write("go", "gotest-empty.json", stream([]ge{{Action: "start", Package: pkgX}}))
	e.write("go", "vitest-zero.json", "")
	e.write("go", "junit-zero.xml", "\n")
	r := e.run()
	eq(t, r.dec.Failures, []failure{
		{"go", "results empty", "gotest-empty.json", "the file holds no test results"},
		{"go", "results empty", "junit-zero.xml", "the file holds no test results"},
		{"go", "results empty", "vitest-zero.json", "the file holds no test results"},
	})
}

func TestRun_SuitesLineThirdFieldMustBeResults(t *testing.T) {
	e := newEnv(t)
	e.writeFile(e.suites, "go=Go=yes\n")
	r := e.run()
	eq(t, r.code, 2)
	eq(t, r.raw == nil, true)
}

const okStep = `[{"name":"extract","status":"passed","duration_ms":12}]`

func TestRun_SectionOkTrueStaysGreen(t *testing.T) {
	e := newEnv(t)
	sec := filepath.Join(e.dir, "ai.json")
	body := `{"ok":true,"accuracy":0.97,"threshold":0.95,"steps":` + okStep + `}`
	e.writeFile(sec, body)
	r := e.run("-section", "ai="+sec)
	eq(t, r.code, 0)
	eq(t, string(r.obj["ai"]), body)
}

func TestRun_SectionOkFalseOrMissingOkIsRed(t *testing.T) {
	for _, body := range []string{`{"ok":false,"accuracy":0.1,"steps":` + okStep + `}`, `{"accuracy":1,"steps":` + okStep + `}`} {
		e := newEnv(t)
		sec := filepath.Join(e.dir, "ai.json")
		e.writeFile(sec, body)
		r := e.run("-section", "ai="+sec)
		eq(t, r.code, 1)
		eq(t, r.dec.Verdict, "red")
		eq(t, string(r.obj["ai"]), body)
	}
}

func TestRun_SectionWithoutStepsIsAFailureAndAbsent(t *testing.T) {
	for _, name := range []string{"ai", "email", "hubspot", "prod_check"} {
		for _, body := range []string{`{"ok":true}`, `{"ok":true,"steps":null}`, `{"ok":true,"steps":[]}`, `{"ok":true,"steps":"x"}`, `{"ok":true,"Steps":[{"name":"a","status":"passed"}]}`} {
			e := newEnv(t)
			sec := filepath.Join(e.dir, name+".json")
			e.writeFile(sec, body)
			r := e.run("-section", name+"="+sec)
			eq(t, r.code, 1)
			eq(t, r.dec.Verdict, "red")
			_, has := r.obj[name]
			eq(t, has, false)
			eq(t, r.dec.Failures[0], failure{name, "section has no steps", sec, "no steps array"})
		}
	}
}

func TestRun_StepWithoutNameOrStatusIsAFailure(t *testing.T) {
	cases := []struct{ steps, msg string }{
		{`[{"name":"a","status":"passed"},{"name":"b"}]`, "step 2 lacks name or status"},
		{`[{"status":"passed"}]`, "step 1 lacks name or status"},
		{`[{"name":"","status":"passed"}]`, "step 1 lacks name or status"},
		{`[{"name":1,"status":"passed"}]`, "step 1 lacks name or status"},
		{`["x"]`, "step 1 lacks name or status"},
		{`[{"name":"a","status":""}]`, "step 1 lacks name or status"},
		{`[{"name":null,"status":"passed"}]`, "step 1 lacks name or status"},
		{`[null]`, "step 1 lacks name or status"},
		{`[{"name":"a","status":"failure"}]`, "step 1 has unknown status"},
		{`[{"name":"a","status":"Failed"}]`, "step 1 has unknown status"},
		{`[{"name":"a","status":"passed"},{"name":"b","status":"error"}]`, "step 2 has unknown status"},
		{`[{"Name":"a","Status":"passed"}]`, "step 1 lacks name or status"},
		{`[{"name":"a","status":"passed"},{"name":"b","status":"passed"},{"name":"c","status":"passed"},{"name":"d","status":1}]`, "step 4 lacks name or status"},
	}
	for _, c := range cases {
		e := newEnv(t)
		sec := filepath.Join(e.dir, "ai.json")
		e.writeFile(sec, `{"ok":true,"steps":`+c.steps+`}`)
		r := e.run("-section", "ai="+sec)
		eq(t, r.code, 1)
		_, has := r.obj["ai"]
		eq(t, has, false)
		eq(t, r.dec.Failures[0], failure{"ai", "section has no steps", sec, c.msg})
	}
}

func TestRun_SectionWithStepsIsCopiedByteForByte(t *testing.T) {
	e := newEnv(t)
	sec := filepath.Join(e.dir, "ai.json")
	body := `{"ok":true,"accuracy":0.97,"threshold":0.95,"steps":[{"name":"a","status":"passed","duration_ms":1},{"name":"b","status":"passed","duration_ms":2,"detail":"x"},{"name":"c","status":"skipped"}]}`
	e.writeFile(sec, body)
	r := e.run("-section", "ai="+sec)
	eq(t, r.code, 0)
	eq(t, string(r.obj["ai"]), body)
}

// The fixture is the bytes e2e/nightlyAi writes; the vitest test main.test.ts owns them.
func TestRun_AISectionFromTheCheckIsAccepted(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "ai", "ai.json"))
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	sec := filepath.Join(e.dir, "ai.json")
	e.writeFile(sec, string(want))
	r := e.run("-section", "ai="+sec)
	eq(t, r.code, 0)
	eq(t, string(r.obj["ai"]), strings.TrimSuffix(string(want), "\n"))

	failed := strings.Replace(string(want), `"name":"explain","status":"passed"`, `"name":"explain","status":"failed"`, 1)
	if failed == string(want) {
		t.Fatal("the fixture has no passed explain step to fail")
	}
	e2 := newEnv(t)
	sec2 := filepath.Join(e2.dir, "ai.json")
	e2.writeFile(sec2, failed)
	r2 := e2.run("-section", "ai="+sec2)
	eq(t, r2.code, 1)
	eq(t, r2.dec.Verdict, "red")
}

func TestRun_SectionWithoutStepsNeedsNoStepsForArgos(t *testing.T) {
	e := newEnv(t)
	sec := filepath.Join(e.dir, "argos.json")
	e.writeFile(sec, `{"ok":true,"build_url":"https://x","changed":4}`)
	r := e.run("-section", "argos="+sec)
	eq(t, r.code, 0)
	_, has := r.obj["argos"]
	eq(t, has, true)
}

func TestRun_FailedStepMakesTheVerdictRed(t *testing.T) {
	for _, ok := range []string{"true", "false"} {
		e := newEnv(t)
		sec := filepath.Join(e.dir, "ai.json")
		body := `{"ok":` + ok + `,"steps":[{"name":"a","status":"failed"}]}`
		e.writeFile(sec, body)
		r := e.run("-section", "ai="+sec)
		eq(t, r.code, 1)
		eq(t, r.dec.Verdict, "red")
		eq(t, string(r.obj["ai"]), body)
		eq(t, len(r.dec.Failures), 0)
	}
}

func TestRun_SkippedStepKeepsTheVerdictGreen(t *testing.T) {
	e := newEnv(t)
	sec := filepath.Join(e.dir, "ai.json")
	e.writeFile(sec, `{"ok":true,"steps":[{"name":"a","status":"passed"},{"name":"b","status":"skipped"}]}`)
	r := e.run("-section", "ai="+sec)
	eq(t, r.code, 0)
	eq(t, r.dec.Verdict, "green")
}

func TestRun_SectionFileMissingIsAbsentAndRed(t *testing.T) {
	e := newEnv(t)
	r := e.run("-section", "ai="+filepath.Join(e.dir, "nope.json"))
	eq(t, r.code, 1)
	_, has := r.obj["ai"]
	eq(t, has, false)
	eq(t, r.dec.Failures[0].Suite, "ai")
	eq(t, r.dec.Failures[0].Test, "section did not run")

	for _, body := range []string{"null", "[1]", "{"} {
		e := newEnv(t)
		sec := filepath.Join(e.dir, "ai.json")
		e.writeFile(sec, body)
		r := e.run("-section", "ai="+sec)
		eq(t, r.code, 1)
		_, has := r.obj["ai"]
		eq(t, has, false)
		eq(t, r.dec.Failures[0].Test, "section did not run")
	}
}

func TestRun_NoSectionFlagWritesNoSectionKeys(t *testing.T) {
	r := newEnv(t).run()
	for _, k := range []string{"ai", "email", "hubspot", "prod_check", "argos"} {
		if _, ok := r.obj[k]; ok {
			t.Fatalf("unexpected key %q", k)
		}
	}
}

func TestRun_UnparseableResultIsRed(t *testing.T) {
	e := newEnv(t)
	e.clear("go")
	e.write("go", "gotest-x.json", `{"Action":`+"\n"+stream(attemptOf(pkgX, "pass", tst(pkgX, "TestA", "pass"))))
	e.clear("frontend")
	e.write("frontend", "vitest-app.json", `{`)
	r := e.run()
	eq(t, r.code, 1)
	eq(t, r.dec.Failures[0].Test, "results unreadable")
	eq(t, r.dec.Failures[0].File, "vitest-app.json")
	eq(t, len(r.dec.Failures), 1)
	eq(t, r.suite("go").Passed, 1)
}

func TestRun_TruncatedGoStreamIsUnreadable(t *testing.T) {
	e := newEnv(t)
	e.clear("go")
	e.write("go", "gotest-x.json", stream(attemptOf(pkgX, "pass", tst(pkgX, "TestA", "pass")))+`{"Action":"pa`)
	r := e.run()
	eq(t, r.code, 1)
	eq(t, len(r.dec.Failures), 1)
	eq(t, r.dec.Failures[0].Test, "results unreadable")
	eq(t, r.dec.Failures[0].File, "gotest-x.json")
	eq(t, r.suite("go").Passed, 1)
}

func TestRun_CorruptMiddleLineOfGoStreamIsNotUnreadable(t *testing.T) {
	e := newEnv(t)
	e.clear("go")
	e.write("go", "gotest-x.json", `{"Action":"pa`+"\n"+stream(attemptOf(pkgX, "pass", tst(pkgX, "TestA", "pass"))))
	r := e.run()
	for _, f := range r.dec.Failures {
		if f.Test == "results unreadable" {
			t.Fatalf("unexpected results unreadable: %+v", f)
		}
	}
	eq(t, r.suite("go").Passed, 1)
}

func TestRun_CutOffFirstRunBeforeARerunIsUnreadable(t *testing.T) {
	e := newEnv(t)
	e.clear("go")
	first := stream(attemptOf(pkgX, "fail", tst(pkgX, "TestA", "fail")))
	first = first[:len(first)-20] + "\n"
	e.write("go", "gotest-x.json", first+stream(attemptOf(pkgX, "pass", tst(pkgX, "TestA", "pass"))))
	r := e.run()
	eq(t, r.code, 1)
	eq(t, r.dec.Failures[0].Test, "results unreadable")
}

func TestRun_GoFileOfOnlyGarbageIsUnreadable(t *testing.T) {
	e := newEnv(t)
	e.clear("go")
	e.write("go", "gotest-x.json", "not json at all\n")
	r := e.run()
	eq(t, r.dec.Failures[0].Test, "results unreadable")
	eq(t, r.dec.Failures[0].File, "gotest-x.json")
}

func TestRun_UsageErrorsReturnTwo(t *testing.T) {
	cases := map[string]func(e *env) []string{
		"missing -out": func(e *env) []string {
			return []string{"-suites", e.suites, "-jobs", e.jobsPath, "-results", e.results}
		},
		"jobs path absent": func(e *env) []string {
			return []string{"-suites", e.suites, "-jobs", filepath.Join(e.dir, "none.json"), "-results", e.results, "-out", e.out}
		},
		"suites line without =": func(e *env) []string {
			e.writeFile(e.suites, "go Go\n")
			return []string{"-suites", e.suites, "-jobs", e.jobsPath, "-results", e.results, "-out", e.out}
		},
		"jobs file not JSON": func(e *env) []string {
			e.writeFile(e.jobsPath, "{")
			return []string{"-suites", e.suites, "-jobs", e.jobsPath, "-results", e.results, "-out", e.out}
		},
		"missing -results": func(e *env) []string {
			return []string{"-suites", e.suites, "-jobs", e.jobsPath, "-out", e.out}
		},
		"positional argument": func(e *env) []string {
			return []string{"-suites", e.suites, "-jobs", e.jobsPath, "-results", e.results, "-out", e.out, "extra"}
		},
		"bad section flag": func(e *env) []string {
			return []string{"-suites", e.suites, "-jobs", e.jobsPath, "-results", e.results, "-out", e.out, "-section", "verdict=x"}
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.run() // writes a valid jobs file; the case then breaks one input
			if err := os.Remove(e.out); err != nil {
				t.Fatal(err)
			}
			args := mk(e)
			r := runArgs(t, e.out, args)
			eq(t, r.code, 2)
			if _, err := os.Stat(e.out); err == nil {
				t.Fatal("output file written on a usage error")
			}
		})
	}
}

func TestRun_MissingResultsDirIsNoResults(t *testing.T) {
	e := newEnv(t)
	if err := os.RemoveAll(e.results); err != nil {
		t.Fatal(err)
	}
	r := e.run()
	eq(t, r.code, 1)
	eq(t, r.failureTests("go"), []string{"no test results"})
	eq(t, r.failureTests("frontend"), []string{"no test results"})
	eq(t, r.failureTests("docling-test"), []string{"no test results"})
	eq(t, r.suite("docker-canary").Passed, 1)
}

const fixtureFlags = "-now 2026-10-29T02:00:00Z -started 2026-10-29T01:17:00Z -run-id 1 -sha abc -workspace /home/runner/work/invoice-os/invoice-os"

func runFixture(t *testing.T, name string) runResult {
	t.Helper()
	d := filepath.Join("testdata", name)
	out := filepath.Join(t.TempDir(), "summary.json")
	args := append([]string{"-suites", filepath.Join("testdata", "suites.txt"), "-jobs", filepath.Join(d, "jobs.json"),
		"-results", filepath.Join(d, "results"), "-out", out}, strings.Fields(fixtureFlags)...)
	return runArgs(t, out, args)
}

func assertWant(t *testing.T, name string, wantCode int) {
	t.Helper()
	r := runFixture(t, name)
	eq(t, r.code, wantCode)
	assertScenariosMatchCounts(t, r)
	var got, want any
	if err := json.Unmarshal(r.raw, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(fixture(t, name, "want.json"), &want); err != nil {
		t.Fatal(err)
	}
	eq(t, got, want)
}

func TestRun_FixtureGreen(t *testing.T)   { assertWant(t, "green", 0) }
func TestRun_FixtureRed(t *testing.T)     { assertWant(t, "red", 1) }
func TestRun_FixtureFlake(t *testing.T)   { assertWant(t, "flake", 0) }
func TestRun_FixtureMissing(t *testing.T) { assertWant(t, "missing", 1) }

func TestRun_FailureOrderIsSuiteThenFileThenTest(t *testing.T) {
	e := newEnv(t)
	other := modulePrefix + "/internal/a"
	e.clear("go")
	e.write("go", "gotest-go.json", stream(
		attemptOf(pkgX, "fail", tst(pkgX, "TestZ", "fail", "    z\n"), tst(pkgX, "TestA", "fail", "    a\n")),
		attemptOf(other, "fail", tst(other, "TestM", "fail", "    m\n"))))
	e.clear("frontend")
	e.write("frontend", "vitest-app.json", vitest("/w/b.test.ts", "failed", "", va("f", "failed", "no")))
	r := e.run()
	order := []string{}
	for _, f := range r.dec.Failures {
		order = append(order, f.Suite+"/"+f.File+"/"+f.Test)
	}
	eq(t, order, []string{"go/internal/a/TestM", "go/internal/x/TestA", "go/internal/x/TestZ", "frontend//w/b.test.ts/f"})
}

func TestRun_ReportLines(t *testing.T) {
	r := runFixture(t, "red")
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	eq(t, lines[0], "nightlysummary: red, 4 suites, 4 failures, 0 flakes")
	n := 0
	for _, l := range lines[1:] {
		if strings.HasPrefix(l, "FAIL ") {
			n++
		}
	}
	eq(t, n, 4)
	eq(t, lines[1], "FAIL go internal/basic TestBad/sub: a_test.go:7: want 1, got 2")

	fl := strings.Split(strings.TrimSpace(runFixture(t, "flake").stdout), "\n")
	eq(t, fl[0], "nightlysummary: green, 4 suites, 0 failures, 2 flakes")
	eq(t, len(fl), 3)
	eq(t, fl[1], "FLAKE frontend frontend/app/src/flake.test.ts flips")
	eq(t, strings.HasPrefix(fl[2], "FLAKE docling-test "), true)
	eq(t, strings.HasSuffix(fl[2], " tests.test_x::test_bad"), true)
}

// ---- Playwright ----

const devEnvWorkflow = ".github/workflows/dev-env.yml"

const pwWorkspace = "/home/runner/work/invoice-os/invoice-os"

func judgePlaywright(t *testing.T, data string) judged {
	t.Helper()
	pf, err := parsePlaywright([]byte(data), pwWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	return split(judgeFiles([]parsedFile{pf}))
}

// pwReport is a one-test report; msg is the error of the last result.
func pwReport(status, msg string) string {
	raw, _ := json.Marshal(msg)
	return fmt.Sprintf(`{"config":{"rootDir":%q},"suites":[{"title":"a.spec.ts","specs":[{"title":"t","file":"a.spec.ts","tests":[{"status":%q,"results":[{"status":"failed","error":{"message":%s}}]}]}]}]}`,
		pwWorkspace+"/e2e", status, raw)
}

func TestPlaywright_StatusesMapToPassFailFlake(t *testing.T) {
	j := judgePlaywright(t, string(fixture(t, "playwright", "mixed.json")))
	eq(t, len(j.passed), 1)
	eq(t, len(j.fails), 1)
	eq(t, len(j.flakes), 1)
}

func TestPlaywright_TitleFileAndMessage(t *testing.T) {
	j := judgePlaywright(t, string(fixture(t, "playwright", "mixed.json")))
	f := j.fails[0]
	eq(t, f.test, "outer › inner › fails twice")
	eq(t, f.file, "e2e/scratch/x.spec.ts")
	eq(t, strings.Contains(f.msg, "\x1b["), false)
	eq(t, strings.Contains(f.msg, "\n"), false)
	eq(t, strings.HasPrefix(f.msg, "Error: expect(received).toBe(expected)"), true)
	eq(t, j.flakes[0].test, "flaky one")
}

func TestPlaywright_MessageClippedAt300(t *testing.T) {
	j := judgePlaywright(t, pwReport("unexpected", strings.Repeat("x", 400)))
	eq(t, len([]rune(j.fails[0].msg)), 300)
}

func TestPlaywright_UnreadableAndEmpty(t *testing.T) {
	e := newEnv(t)
	e.clear("frontend")
	e.write("frontend", "playwright-a.json", "{")
	e.write("frontend", "playwright-b.json", `{"config":{"rootDir":"/x"},"suites":[]}`)
	r := e.run()
	eq(t, r.code, 1)
	eq(t, r.failureTests("frontend"), []string{"results unreadable", "results empty"})
}

func TestPlaywright_SkippedOnlyReportIsNotEmpty(t *testing.T) {
	j := judgePlaywright(t, pwReport("skipped", ""))
	eq(t, len(j.fails)+len(j.passed)+len(j.flakes), 0)
}

// ---- -e2e-suites ----

const e2eSuites = "environment=Prepare the preview (secrets, bucket, URLs)\ne2e=E2E=results\n"

func (e *env) e2e(suites string) {
	e.t.Helper()
	p := filepath.Join(e.dir, "e2e-suites.txt")
	e.writeFile(p, suites)
	e.e2eFlag = []string{"-e2e-suites", p}
}

func TestE2ESuites_OrderAndWorkflowFile(t *testing.T) {
	e := newEnv(t)
	e.e2e(e2eSuites)
	e.jobs = append(e.jobs, job{Name: "Prepare the preview (secrets, bucket, URLs)", Conclusion: "success"})
	r := e.run(e.e2eFlag...)
	eq(t, r.code, 1)
	names := []string{}
	for _, s := range r.dec.Suites {
		names = append(names, s.Name)
	}
	eq(t, names, []string{"go", "frontend", "docling-test", "docker-canary", "environment"})
	eq(t, r.dec.Failures, []failure{{"e2e", "suite did not run", devEnvWorkflow, "job E2E missing"}})
}

func TestE2ESuites_PreviewCapReached(t *testing.T) {
	e := newEnv(t)
	e.e2e(e2eSuites)
	e.jobs = append(e.jobs, job{
		Name: "Prepare the preview (secrets, bucket, URLs)", Conclusion: "failure",
		Steps: steps("Set up job", "success", "preview cap reached", "failure"),
	})
	r := e.run(e.e2eFlag...)
	eq(t, r.code, 1)
	eq(t, r.dec.Verdict, "red")
	eq(t, r.dec.Failures[0], failure{"environment", "preview cap reached", devEnvWorkflow, "step failure"})
}

func TestE2ESuites_KeyAlsoInCISuitesIsUsageError(t *testing.T) {
	e := newEnv(t)
	e.e2e("go=Go again\n")
	r := e.run(e.e2eFlag...)
	eq(t, r.code, 2)
}

func TestE2ESuites_NoResultsAndJobEntriesNameDevEnv(t *testing.T) {
	e := newEnv(t)
	e.e2e(e2eSuites)
	e.jobs = append(e.jobs,
		job{Name: "Prepare the preview (secrets, bucket, URLs)", Conclusion: "failure"},
		job{Name: "E2E", Conclusion: "success"})
	r := e.run(e.e2eFlag...)
	eq(t, r.code, 1)
	eq(t, r.dec.Failures, []failure{
		{"environment", "(job)", devEnvWorkflow, "job failure"},
		{"e2e", "no test results", devEnvWorkflow, "job E2E wrote no results"},
	})
}

func TestPlaywright_MessageComesFromTheLastFailingAttempt(t *testing.T) {
	rep := `{"config":{"rootDir":"/w/e2e"},"suites":[{"title":"a.spec.ts","specs":[{"title":"t","file":"a.spec.ts","tests":[{"status":"unexpected","results":[` +
		`{"status":"failed","error":{"message":"first attempt"}},{"status":"failed","error":{"message":"last attempt\nsecond line"}}]}]}]}]}`
	j := judgePlaywright(t, rep)
	eq(t, len(j.fails), 1)
	eq(t, j.fails[0].msg, "last attempt")
}

func TestPlaywright_SkippedOnlyReportIsNotResultsEmpty(t *testing.T) {
	e := newEnv(t)
	e.clear("frontend")
	e.write("frontend", "playwright-a.json", pwReport("skipped", ""))
	r := e.run()
	eq(t, r.code, 0)
	eq(t, len(r.dec.Failures), 0)
}

func runE2EFixture(t *testing.T) runResult {
	t.Helper()
	d := filepath.Join("testdata", "e2e")
	out := filepath.Join(t.TempDir(), "summary.json")
	args := append([]string{"-suites", filepath.Join("testdata", "suites.txt"), "-e2e-suites", filepath.Join(d, "e2e-suites.txt"),
		"-jobs", filepath.Join(d, "jobs.json"), "-results", filepath.Join(d, "results"), "-out", out}, strings.Fields(fixtureFlags)...)
	return runArgs(t, out, args)
}

func TestRun_E2EWholeRun(t *testing.T) {
	r := runE2EFixture(t)
	eq(t, r.code, 1)
	eq(t, r.dec.Verdict, "red")
	eq(t, *r.suite("environment"), suiteResult{Name: "environment", Passed: 1})
	eq(t, *r.suite("e2e"), suiteResult{Name: "e2e", Passed: 1, Flaky: 1})
	eq(t, *r.suite("topology-auth"), suiteResult{Name: "topology-auth", Failed: 1})
	eq(t, r.dec.Flakes, []flake{{"e2e", "flaky one", "e2e/scratch/x.spec.ts"}})
	eq(t, r.dec.Failures, []failure{{"topology-auth", "outer › inner › fails twice", "e2e/scratch/x.spec.ts", r.dec.Failures[0].Message}})
	eq(t, strings.HasPrefix(r.dec.Failures[0].Message, "Error: expect(received)"), true)
	assertScenariosMatchCounts(t, r, "e2e", "topology-auth")
}

func TestRun_FixtureScenariosAndSteps(t *testing.T) {
	d := filepath.Join("testdata", "e2e")
	out := filepath.Join(t.TempDir(), "summary.json")
	args := append([]string{"-suites", filepath.Join("testdata", "suites.txt"), "-e2e-suites", filepath.Join(d, "e2e-suites.txt"),
		"-jobs", filepath.Join(d, "jobs.json"), "-results", filepath.Join(d, "results"), "-out", out,
		"-section", "ai=" + filepath.Join(d, "ai.json")}, strings.Fields(fixtureFlags)...)
	r := runArgs(t, out, args)
	eq(t, r.code, 1)
	want, err := os.ReadFile(filepath.Join(d, "want.json"))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, string(r.raw), string(want))
}

// ---- scenarios ----

func TestPlaywright_ScenarioFieldsFromMixedReport(t *testing.T) {
	pf, err := parsePlaywright(fixture(t, "playwright", "mixed.json"), pwWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		test       string
		status     status
		attempts   int
		durationMS int64
	}
	got := []row{}
	for _, o := range pf.direct {
		got = append(got, row{o.test, o.status, o.attempts, o.durationMS})
	}
	eq(t, got, []row{
		{"passes", statusPass, 1, 3},
		{"flaky one", statusFlake, 2, 5},
		{"skipped one", statusSkip, 1, 0},
		{"outer › inner › fails twice", statusFail, 2, 8},
	})
}

// pwSpec is one spec of one test; results are (status, duration) pairs.
func pwSpec(title, testStatus string, results ...string) string {
	return fmt.Sprintf(`{"title":%q,"file":"a.spec.ts","tests":[{"status":%q,"results":[%s]}]}`, title, testStatus, strings.Join(results, ","))
}

func pwResult(status string, duration float64) string {
	return fmt.Sprintf(`{"status":%q,"duration":%v,"error":{"message":"boom"}}`, status, duration)
}

func pwFile(specs ...string) string {
	return fmt.Sprintf(`{"config":{"rootDir":%q},"suites":[{"title":"a.spec.ts","specs":[%s]}]}`, pwWorkspace+"/e2e", strings.Join(specs, ","))
}

func TestPlaywright_InterruptedOnlyTestIsSkipped(t *testing.T) {
	pf, err := parsePlaywright([]byte(pwFile(pwSpec("t", "skipped", pwResult("interrupted", 0)))), pwWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, len(pf.direct), 1)
	eq(t, pf.direct[0].status, statusSkip)
	eq(t, pf.direct[0].attempts, 1)
}

func TestPlaywright_FractionalDurationRounds(t *testing.T) {
	pf, err := parsePlaywright([]byte(pwFile(pwSpec("t", "flaky", pwResult("failed", 1.4), pwResult("passed", 2.2)))), pwWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, pf.direct[0].durationMS, int64(4))
}

func e2eEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.e2e("e2e=E2E=results\ntopology-auth=E2E topology (auth)=results\n")
	e.jobs = append(e.jobs, job{Name: "E2E", Conclusion: "success"}, job{Name: "E2E topology (auth)", Conclusion: "success"})
	return e
}

func TestRun_ScenariosMatchFailuresAndFlakes(t *testing.T) {
	r := runE2EFixture(t)
	by := map[string]scenario{}
	for _, sc := range r.scenarios {
		by[sc.Status] = sc
	}
	f := r.dec.Failures[0]
	eq(t, by["failed"], scenario{Suite: f.Suite, Test: f.Test, File: f.File, Status: "failed", Attempts: 2, DurationMS: 9, Message: f.Message})
	fl := r.dec.Flakes[0]
	eq(t, [3]string{by["flaky"].Suite, by["flaky"].Test, by["flaky"].File}, [3]string{fl.Suite, fl.Test, fl.File})
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(r.obj["scenarios"], &raw); err != nil {
		t.Fatal(err)
	}
	for _, m := range raw {
		_, has := m["message"]
		eq(t, has, string(m["status"]) == `"failed"`)
	}
}

func TestRun_ScenariosOnlyForE2ESuites(t *testing.T) {
	e := newEnv(t)
	e.write("frontend", "playwright-a.json", pwFile(pwSpec("t", "expected", pwResult("passed", 1))))
	r := e.run()
	eq(t, r.code, 0)
	if !strings.Contains(string(r.raw), `"scenarios":[]`) {
		t.Fatalf("scenarios not empty in %s", r.raw)
	}
}

func TestRun_ScenariosKeepSuiteFileReportOrder(t *testing.T) {
	e := e2eEnv(t)
	one := func(title string) string { return pwFile(pwSpec(title, "expected", pwResult("passed", 1))) }
	e.write("e2e", "playwright-smoke.json", pwFile(
		pwSpec("smoke 1", "expected", pwResult("passed", 1)), pwSpec("smoke 2", "expected", pwResult("passed", 1))))
	e.write("e2e", "playwright-api.json", one("api 1"))
	e.write("topology-auth", "playwright-topology-auth.json", one("auth 1"))
	r := e.run(e.e2eFlag...)
	eq(t, r.code, 0)
	got := []string{}
	for _, sc := range r.scenarios {
		got = append(got, sc.Suite+"/"+sc.Test)
	}
	eq(t, got, []string{"e2e/api 1", "e2e/smoke 1", "e2e/smoke 2", "topology-auth/auth 1"})
}

func TestRun_DuplicateTitlesAreSeparateScenarios(t *testing.T) {
	e := e2eEnv(t)
	e.write("e2e", "playwright-e2e.json", pwFile(pwSpec("loop", "expected", pwResult("passed", 1)), pwSpec("loop", "expected", pwResult("passed", 2))))
	e.write("topology-auth", "playwright-topology-auth.json", pwFile(pwSpec("t", "expected", pwResult("passed", 1))))
	r := e.run(e.e2eFlag...)
	eq(t, len(r.scenarios), 3)
	eq(t, r.scenarios[0].Test, r.scenarios[1].Test)
	eq(t, r.scenarios[1].DurationMS, int64(2))
}

func TestRun_E2EJobThatDidNotRunHasNoScenarios(t *testing.T) {
	e := e2eEnv(t)
	e.job("E2E").Conclusion = "skipped"
	e.write("e2e", "playwright-e2e.json", pwFile(pwSpec("t", "expected", pwResult("passed", 1))))
	e.write("topology-auth", "playwright-topology-auth.json", pwFile(pwSpec("u", "expected", pwResult("passed", 1))))
	r := e.run(e.e2eFlag...)
	eq(t, r.code, 1)
	eq(t, r.dec.Failures, []failure{{"e2e", "suite did not run", devEnvWorkflow, "job E2E skipped"}})
	eq(t, len(r.scenarios), 1)
	eq(t, r.scenarios[0].Suite, "topology-auth")
}

func TestRun_ScenarioCountsMatchSuiteCounts(t *testing.T) {
	for _, name := range []string{"green", "red", "flake", "missing"} {
		assertScenariosMatchCounts(t, runFixture(t, name))
	}
	assertScenariosMatchCounts(t, runE2EFixture(t), "e2e", "topology-auth")
}

func TestRun_FailedJobStepFailuresAreNotScenarios(t *testing.T) {
	e := e2eEnv(t)
	j := e.job("E2E")
	j.Conclusion = "failure"
	j.Steps = steps("Run the suite", "failure", "Upload", "skipped")
	e.write("e2e", "playwright-e2e.json", pwFile(pwSpec("bad", "unexpected", pwResult("failed", 1))))
	e.write("topology-auth", "playwright-topology-auth.json", pwFile(pwSpec("t", "expected", pwResult("passed", 1))))
	r := e.run(e.e2eFlag...)
	failed := 0
	for _, sc := range r.scenarios {
		if sc.Status == "failed" {
			failed++
		}
	}
	eq(t, failed, 1)
	eq(t, r.suite("e2e").Failed, 2)
	eq(t, r.failureTests("e2e"), []string{"Upload", "bad"})
}

func TestRun_E2ESuiteOfOnlySkippedTestsIsRed(t *testing.T) {
	e := e2eEnv(t)
	e.write("e2e", "playwright-e2e.json", pwFile(pwSpec("skipped", "skipped", pwResult("skipped", 0))))
	e.write("topology-auth", "playwright-topology-auth.json", pwFile(pwSpec("t", "expected", pwResult("passed", 1))))
	r := e.run(e.e2eFlag...)
	eq(t, r.code, 1)
	eq(t, r.dec.Failures, []failure{{"e2e", "no test results", "results-e2e/playwright-e2e.json", "no passed, failed or flaky test in the report"}})
	eq(t, r.scenarios[0].Status, "skipped")
	eq(t, *r.suite("e2e"), suiteResult{Name: "e2e", Failed: 1})
}

func TestRun_E2EOnlyInterruptedTestsIsRed(t *testing.T) {
	e := e2eEnv(t)
	e.write("e2e", "playwright-e2e.json", pwFile(pwSpec("cut", "skipped", pwResult("interrupted", 0))))
	e.write("topology-auth", "playwright-topology-auth.json", pwFile(pwSpec("t", "expected", pwResult("passed", 1))))
	r := e.run(e.e2eFlag...)
	eq(t, r.code, 1)
	eq(t, r.failureTests("e2e"), []string{"no test results"})
}

func TestRun_E2EOneRanTestBesideSkippedIsGreen(t *testing.T) {
	e := e2eEnv(t)
	e.write("e2e", "playwright-e2e.json", pwFile(
		pwSpec("ran", "expected", pwResult("passed", 1)), pwSpec("skipped", "skipped", pwResult("skipped", 0))))
	e.write("topology-auth", "playwright-topology-auth.json", pwFile(pwSpec("t", "expected", pwResult("passed", 1))))
	r := e.run(e.e2eFlag...)
	eq(t, r.code, 0)
	eq(t, len(r.dec.Failures), 0)
	eq(t, len(r.scenarios), 3)
	eq(t, r.scenarios[1].Status, "skipped")
	eq(t, strings.Contains(string(r.obj["scenarios"]), "message"), false)
}

func TestRun_E2EReportsAreJudgedOneByOne(t *testing.T) {
	e := e2eEnv(t)
	e.write("e2e", "playwright-api.json", `{"config":{"rootDir":"/x"},"suites":[]}`)
	e.write("e2e", "playwright-smoke.json", pwFile(pwSpec("skipped", "skipped", pwResult("skipped", 0))))
	e.write("e2e", "playwright-zeta.json", pwFile(pwSpec("skipped", "skipped", pwResult("skipped", 0))))
	e.write("topology-auth", "playwright-topology-auth.json", pwFile(pwSpec("t", "expected", pwResult("passed", 1))))
	r := e.run(e.e2eFlag...)
	eq(t, r.code, 1)
	eq(t, r.dec.Failures, []failure{
		{"e2e", "results empty", "playwright-api.json", "the file holds no test results"},
		{"e2e", "no test results", "results-e2e/playwright-smoke.json", "no passed, failed or flaky test in the report"},
		{"e2e", "no test results", "results-e2e/playwright-zeta.json", "no passed, failed or flaky test in the report"},
	})
}

func TestRun_E2EAllSkippedReportBesideAPassingOneIsRed(t *testing.T) {
	e := e2eEnv(t)
	e.write("e2e", "playwright-api.json", pwFile(pwSpec("ran", "expected", pwResult("passed", 1))))
	e.write("e2e", "playwright-smoke.json", pwFile(pwSpec("skipped", "skipped", pwResult("skipped", 0))))
	e.write("topology-auth", "playwright-topology-auth.json", pwFile(pwSpec("t", "expected", pwResult("passed", 1))))
	r := e.run(e.e2eFlag...)
	eq(t, r.code, 1)
	eq(t, r.dec.Failures, []failure{{"e2e", "no test results", "results-e2e/playwright-smoke.json", "no passed, failed or flaky test in the report"}})
}

func TestRun_E2ESuiteOfOnlyFlakyTestsIsGreen(t *testing.T) {
	e := e2eEnv(t)
	e.write("e2e", "playwright-e2e.json", pwFile(pwSpec("flaky", "flaky", pwResult("failed", 1), pwResult("passed", 1))))
	e.write("topology-auth", "playwright-topology-auth.json", pwFile(pwSpec("t", "expected", pwResult("passed", 1))))
	r := e.run(e.e2eFlag...)
	eq(t, r.code, 0)
	eq(t, len(r.dec.Failures), 0)
	eq(t, r.scenarios[0].Status, "flaky")
}

const emailSuites = "environment=Prepare the preview (secrets, bucket, URLs)\nemail=Real invite and reset mail\n"

func TestRun_EmailSectionFromTheCheckIsCopied(t *testing.T) {
	names := []string{"mail budget", "invite mail arrives", "invite text matches", "invite page registers the invitee", "password mail arrives",
		"password mail text matches", "password link opens the workspace", "used invite link is refused", "reset mail arrives",
		"reset text matches", "reset link sets the password", "sign-in with the new password", "only these mails are real"}
	var parts []string
	for _, n := range names {
		parts = append(parts, `{"name":"`+n+`","status":"passed","duration_ms":3}`)
	}
	body := `{"ok":true,"steps":[` + strings.Join(parts, ",") + `],"failures":[]}`
	e := newEnv(t)
	sec := filepath.Join(e.dir, "email.json")
	e.writeFile(sec, body)
	r := e.run("-section", "email="+sec)
	eq(t, r.code, 0)
	eq(t, string(r.obj["email"]), body)
}

func TestRun_MissingSecretsReadAsARedEmailSuite(t *testing.T) {
	e := newEnv(t)
	e.e2e(emailSuites)
	e.jobs = append(e.jobs,
		job{Name: "Prepare the preview (secrets, bucket, URLs)", Conclusion: "success"},
		job{Name: "Real invite and reset mail", Conclusion: "failure",
			Steps: steps("Set up job", "success", "Check the nightly mail secrets", "failure", "Turn real mail on", "skipped", "Real invite and reset mail", "skipped")})
	r := e.run(append(e.e2eFlag, "-section", "email="+filepath.Join(e.dir, "nope.json"))...)
	eq(t, r.code, 1)
	eq(t, r.dec.Verdict, "red")
	var got []string
	for _, f := range r.dec.Failures {
		got = append(got, f.Suite+"|"+f.Test)
	}
	if !slices.Contains(got, "email|Check the nightly mail secrets") || !slices.Contains(got, "email|section did not run") {
		t.Errorf("failures %q lack the step and 'section did not run'", got)
	}
}
