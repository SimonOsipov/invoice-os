package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type call struct {
	stream string
	code   int
}

// fake answers the i-th runner call with calls[i] and records every argv.
type fake struct {
	calls []call
	argvs []string
}

func (f *fake) run(args []string, out io.Writer) int {
	i := len(f.argvs)
	f.argvs = append(f.argvs, strings.Join(args, " "))
	if i >= len(f.calls) {
		return 0
	}
	io.WriteString(out, f.calls[i].stream)
	return f.calls[i].code
}

// ev renders one test2json line; test "" is a package-level event.
func ev(action, pkg, test, output string) string {
	s := fmt.Sprintf(`{"Action":%q,"Package":%q`, action, pkg)
	if test != "" {
		s += fmt.Sprintf(`,"Test":%q`, test)
	}
	if output != "" {
		s += fmt.Sprintf(`,"Output":%q`, output)
	}
	return s + "}\n"
}

func invoke(t *testing.T, f *fake, goArgs ...string) (code int, out string, stderr string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "r.jsonl")
	var eb bytes.Buffer
	code = run(append([]string{"-out", path, "--"}, goArgs...), io.Discard, &eb, f.run)
	b, _ := os.ReadFile(path)
	return code, string(b), eb.String()
}

func TestRun_GreenRunsOnce(t *testing.T) {
	stream := ev("pass", "x", "TestA", "") + ev("pass", "x", "TestB", "") + ev("pass", "x", "", "")
	f := &fake{calls: []call{{stream, 0}}}
	code, out, _ := invoke(t, f, "./x/...")
	if code != 0 || len(f.argvs) != 1 || f.argvs[0] != "test -json ./x/..." || out != stream {
		t.Fatalf("code=%d argvs=%q out=%q", code, f.argvs, out)
	}
}

func twoPkgStream() string {
	return ev("fail", "pkg/a", "TestA", "") + ev("pass", "pkg/a", "TestB", "") + ev("fail", "pkg/a", "", "") +
		ev("fail", "pkg/b", "TestC", "") + ev("fail", "pkg/b", "TestD/sub", "") + ev("fail", "pkg/b", "TestD", "") + ev("fail", "pkg/b", "", "")
}

func TestRun_RetryPassExitsZero(t *testing.T) {
	r1, r2 := ev("pass", "pkg/a", "TestA", ""), ev("pass", "pkg/b", "TestC", "")
	f := &fake{calls: []call{{twoPkgStream(), 1}, {r1, 0}, {r2, 0}}}
	code, out, stderr := invoke(t, f, "./...")
	want := []string{"test -json ./...", "test -json -count=1 -run ^(TestA)$ pkg/a", "test -json -count=1 -run ^(TestC|TestD)$ pkg/b"}
	if code != 0 || strings.Join(f.argvs, "\n") != strings.Join(want, "\n") {
		t.Fatalf("code=%d argvs=%q", code, f.argvs)
	}
	if out != twoPkgStream()+r1+r2 {
		t.Errorf("out file = %q", out)
	}
	if !strings.Contains(stderr, "gotestretry: re-run pkg/a ^(TestA)$") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestRun_RetryFailExitsOne(t *testing.T) {
	for _, codes := range [][2]int{{0, 1}, {1, 0}, {1, 1}} {
		f := &fake{calls: []call{{twoPkgStream(), 1}, {"", codes[0]}, {"", codes[1]}}}
		code, _, _ := invoke(t, f, "./...")
		if code != 1 || len(f.argvs) != 3 {
			t.Fatalf("rerun exits %v: code=%d argvs=%q", codes, code, f.argvs)
		}
	}
}

func TestRun_SubtestOnlyFailureRerunsItsTopLevelTest(t *testing.T) {
	s := ev("fail", "p", "TestE/case", "") + ev("fail", "p", "TestE", "") + ev("fail", "p", "", "")
	f := &fake{calls: []call{{s, 1}}}
	invoke(t, f, "./p")
	if len(f.argvs) != 2 || f.argvs[1] != "test -json -count=1 -run ^(TestE)$ p" {
		t.Fatalf("argvs=%q", f.argvs)
	}
}

func TestRun_BuildFailureIsNotRetried(t *testing.T) {
	s := `{"Action":"fail","Package":"build-fail","FailedBuild":"build-fail"}` + "\n" +
		ev("fail", "pkg/a", "TestA", "") + ev("fail", "pkg/a", "", "")
	f := &fake{calls: []call{{s, 1}, {"", 0}}}
	code, _, _ := invoke(t, f, "./...")
	if code != 1 || len(f.argvs) != 2 || !strings.HasSuffix(f.argvs[1], " pkg/a") {
		t.Fatalf("code=%d argvs=%q", code, f.argvs)
	}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRun_PanicRerunsWholePackage(t *testing.T) {
	for _, tc := range []struct{ rerun, want int }{{0, 0}, {1, 1}} {
		f := &fake{calls: []call{{fixture(t, "panic.jsonl"), 1}, {"", tc.rerun}}}
		code, _, _ := invoke(t, f, "-count=1", "-run", "Test", "./pan/")
		if code != tc.want || len(f.argvs) != 2 || f.argvs[1] != "test -json -count=1 -run Test example.com/gj/pan" {
			t.Fatalf("rerun=%d: code=%d argvs=%q", tc.rerun, code, f.argvs)
		}
	}
}

func TestRun_TimeoutRerunsWholePackage(t *testing.T) {
	f := &fake{calls: []call{{fixture(t, "timeout.jsonl"), 1}, {"", 1}}}
	code, _, _ := invoke(t, f, "-timeout", "1s", "./tmo/")
	if code != 1 || len(f.argvs) != 2 || f.argvs[1] != "test -json -count=1 example.com/gj/tmo" {
		t.Fatalf("code=%d argvs=%q", code, f.argvs)
	}
}

func TestRun_StartedTestWithoutResultRerunsWholePackage(t *testing.T) {
	s := ev("run", "p", "TestX", "") + ev("fail", "p", "", "")
	f := &fake{calls: []call{{s, 1}, {"", 0}}}
	code, _, _ := invoke(t, f, "./p")
	if code != 0 || len(f.argvs) != 2 || f.argvs[1] != "test -json -count=1 p" {
		t.Fatalf("code=%d argvs=%q", code, f.argvs)
	}
}

func TestRun_PackageFailWithNoFailedTestRerunsWholePackage(t *testing.T) {
	s := ev("output", "p", "", "FAIL\tp\t0.1s\n") + ev("fail", "p", "", "")
	for _, rerun := range []int{0, 1} {
		f := &fake{calls: []call{{s, 1}, {"", rerun}}}
		code, _, _ := invoke(t, f, "./p")
		if code != rerun || len(f.argvs) != 2 || f.argvs[1] != "test -json -count=1 p" {
			t.Fatalf("rerun=%d: code=%d argvs=%q", rerun, code, f.argvs)
		}
	}
}

func TestRun_MixedPackagesPickTheirOwnRerun(t *testing.T) {
	s := ev("fail", "pkg/b", "TestB", "") + ev("output", "pkg/b", "TestB", "panic: x\n") + ev("fail", "pkg/b", "", "") +
		ev("fail", "pkg/a", "TestA", "") + ev("fail", "pkg/a", "", "")
	f := &fake{calls: []call{{s, 1}}}
	invoke(t, f, "./...")
	if len(f.argvs) != 3 || f.argvs[1] != "test -json -count=1 -run ^(TestA)$ pkg/a" || f.argvs[2] != "test -json -count=1 pkg/b" {
		t.Fatalf("argvs=%q", f.argvs)
	}
}

func TestRun_FlagsCarried(t *testing.T) {
	plain := ev("fail", "p", "TestX", "") + ev("fail", "p", "", "")
	f := &fake{calls: []call{{plain, 1}}}
	invoke(t, f, "-p", "1", "-count=1", "-tags", "mockissuer", "-race", "-run", "TestX", "./internal/...")
	if got := f.argvs[1]; got != "test -json -count=1 -run ^(TestX)$ -tags mockissuer -race p" {
		t.Errorf("plain re-run = %q", got)
	}

	boom := ev("run", "p", "TestX", "") + ev("output", "p", "TestX", "panic: x\n") + ev("fail", "p", "", "")
	f = &fake{calls: []call{{boom, 1}}}
	invoke(t, f, "-p", "1", "-count=1", "-tags", "mockissuer", "-race", "-run", "TestX", "./internal/...")
	if got := f.argvs[1]; got != "test -json -count=1 -tags mockissuer -race -run TestX p" {
		t.Errorf("whole re-run = %q", got)
	}

	f = &fake{calls: []call{{plain, 1}}}
	invoke(t, f, "-tags=a,b", "./p")
	if got := f.argvs[1]; got != "test -json -count=1 -run ^(TestX)$ -tags=a,b p" {
		t.Errorf("-tags= re-run = %q", got)
	}
}

func TestRun_UsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"--", "./..."},
		{"-out", "f", "./..."},
		{"-out", "f", "--"},
		{"-out", "f", "stray", "--", "./..."},
	} {
		f := &fake{}
		code := run(args, io.Discard, io.Discard, f.run)
		if code != 2 || len(f.argvs) != 0 {
			t.Errorf("args %q: code=%d runner calls=%d", args, code, len(f.argvs))
		}
	}
}

func TestRun_UnfinishedTestAlongsideFailedTestRerunsWholePackage(t *testing.T) {
	s := ev("fail", "p", "TestA", "") + ev("run", "p", "TestB", "") + ev("fail", "p", "", "")
	f := &fake{calls: []call{{s, 1}, {"", 0}}}
	code, _, stderr := invoke(t, f, "./p")
	if code != 0 || len(f.argvs) != 2 || f.argvs[1] != "test -json -count=1 p" {
		t.Fatalf("code=%d argvs=%q", code, f.argvs)
	}
	if !strings.Contains(stderr, "gotestretry: re-run p whole package") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestRun_FailureWithNoPackageFailEventStaysRed(t *testing.T) {
	s := ev("pass", "p", "TestA", "")
	f := &fake{calls: []call{{s, 1}, {"", 0}}}
	code, _, _ := invoke(t, f, "./p")
	if code != 1 || len(f.argvs) != 1 {
		t.Fatalf("code=%d argvs=%q", code, f.argvs)
	}
}

func TestRun_TruncatesExistingOutFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.jsonl")
	if err := os.WriteFile(path, []byte("stale stale stale stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stream := ev("pass", "x", "TestA", "")
	f := &fake{calls: []call{{stream, 0}}}
	if code := run([]string{"-out", path, "--", "./x"}, io.Discard, io.Discard, f.run); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if b, _ := os.ReadFile(path); string(b) != stream {
		t.Fatalf("out = %q", b)
	}
}
