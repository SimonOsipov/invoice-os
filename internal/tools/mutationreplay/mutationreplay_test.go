package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCountNeedle(t *testing.T) {
	src := []byte("alpha beta alpha")
	for needle, want := range map[string]int{"gamma": 0, "beta": 1, "alpha": 2, "": 0} {
		if got := CountNeedle(src, needle); got != want {
			t.Errorf("CountNeedle(%q) = %d, want %d", needle, got, want)
		}
	}
	// Overlapping matches make the replacement site ambiguous too.
	if got := CountNeedle([]byte("aaa"), "aa"); got != 2 {
		t.Errorf("overlapping count = %d, want 2", got)
	}
}

func TestCheck(t *testing.T) {
	src := []byte("return a + b\nreturn a - b\nreturn a - b\n")
	row := Row{File: "x.go", TestFile: "x_test.go", Test: "TestX"}

	cases := []struct {
		find, replace, wantErr string
	}{
		{"return a * b", "x", "find occurs 0 times"},
		{"return a - b", "x", "find occurs 2 times"},
		{"return a + b", "return a + b", "find == replace"},
		{"return a + b", "return a - b", ""},
	}
	for _, c := range cases {
		r := row
		r.Find, r.Replace = c.find, c.replace
		kind, err := Check(r, src)
		if c.wantErr == "" {
			if err != nil || kind != KindGo {
				t.Errorf("Check(%q) = %v, %v; want KindGo, nil", c.find, kind, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("Check(%q) err = %v, want containing %q", c.find, err, c.wantErr)
		}
	}
}

func TestDetectKind(t *testing.T) {
	ok := map[string]Kind{
		"internal/money/money_test.go":           KindGo,
		"frontend/app/src/lib/route.test.ts":     KindVitest,
		"frontend/app/src/components/X.test.tsx": KindVitest,
		"e2e/topology/layout.test.ts":            KindVitest,
	}
	for path, want := range ok {
		if got, err := DetectKind(path); err != nil || got != want {
			t.Errorf("DetectKind(%s) = %v, %v; want %v", path, got, err, want)
		}
	}

	for _, path := range []string{"e2e/smoke/support-console.spec.ts", "e2e/topology/roles.spec.ts"} {
		_, err := DetectKind(path)
		if err == nil || err.Error() != playwrightReason {
			t.Errorf("DetectKind(%s) err = %v, want the Playwright reason", path, err)
		}
	}
	for _, path := range []string{"frontend/app/src/lib/route.ts", "tools/x.py", "frontend/app/src/x.spec.ts"} {
		_, err := DetectKind(path)
		if err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Errorf("DetectKind(%s) err = %v, want unsupported", path, err)
		}
	}
}

func TestGoRunPattern(t *testing.T) {
	for in, want := range map[string]string{
		"TestParse":               "^TestParse$",
		"TestParse/empty input":   "^TestParse$/^empty_input$",
		"TestParse/a.b(c)/nested": `^TestParse$/^a\.b\(c\)$/^nested$`,
	} {
		if got := GoRunPattern(in); got != want {
			t.Errorf("GoRunPattern(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGoOutcome(t *testing.T) {
	cases := []struct {
		name, test, output string
		want               Outcome
	}{
		{"pass", "TestAdd", "=== RUN   TestAdd\n--- PASS: TestAdd (0.00s)\nPASS\nok  \tpkg\t0.2s\n", Passed},
		{"fail", "TestAdd", "=== RUN   TestAdd\n    add_test.go:9: got 1\n--- FAIL: TestAdd (0.00s)\nFAIL\nFAIL\tpkg\t0.2s\n", Failed},
		{"prefix is not the test", "TestAdd", "--- FAIL: TestAddMore (0.00s)\n--- PASS: TestAdd (0.00s)\n", Passed},
		{"subtest fail", "TestAdd/two negatives", "--- FAIL: TestAdd (0.00s)\n    --- FAIL: TestAdd/two_negatives (0.00s)\n", Failed},
		{"sibling subtest fail only", "TestAdd/two negatives", "--- FAIL: TestAdd (0.00s)\n    --- PASS: TestAdd/two_negatives (0.00s)\n    --- FAIL: TestAdd/other (0.00s)\n", Passed},
		{"build failed", "TestAdd", "# pkg\n./add.go:3:9: undefined: c\nFAIL\tpkg [build failed]\nFAIL\n", BuildFailed},
		{"setup failed", "TestAdd", "FAIL\tpkg [setup failed]\n", BuildFailed},
		{"no tests ran", "TestAdd", "testing: warning: no tests to run\nPASS\nok  \tpkg\t0.2s [no tests to run]\n", NotRun},
		{"skipped", "TestAdd", "--- SKIP: TestAdd (0.00s)\nPASS\n", NotRun},
	}
	for _, c := range cases {
		if got := GoOutcome(c.output, c.test); got != c.want {
			t.Errorf("%s: GoOutcome = %v, want %v", c.name, got, c.want)
		}
	}
}

// Shapes taken from a real `vitest run --reporter=json` report.
func TestVitestOutcome(t *testing.T) {
	const file = "/repo/frontend/app/src/lib/actor.test.ts"
	report := func(fileStatus, assertions string) []byte {
		return []byte(`{"success":true,"testResults":[{"name":"` + file + `","status":"` + fileStatus +
			`","message":"","assertionResults":[` + assertions + `]}]}`)
	}
	const (
		target  = `{"title":"renders system","fullName":"actorLabel renders system","ancestorTitles":["actorLabel"],"status":"%s"}`
		sibling = `{"title":"other","fullName":"actorLabel other","ancestorTitles":["actorLabel"],"status":"%s"}`
	)
	a := func(tpl, status string) string { return strings.Replace(tpl, "%s", status, 1) }

	cases := []struct {
		name, test string
		report     []byte
		want       Outcome
	}{
		{"passed by full name", "actorLabel renders system", report("passed", a(target, "passed")+","+a(sibling, "skipped")), Passed},
		{"passed by title", "renders system", report("passed", a(target, "passed")), Passed},
		{"failed", "renders system", report("failed", a(target, "failed")), Failed},
		{"sibling failure does not count", "renders system", report("failed", a(target, "passed")+","+a(sibling, "failed")), Passed},
		{"filtered out", "renders system", report("passed", a(target, "skipped")), NotRun},
		{"no such test", "missing", report("passed", a(target, "passed")), NotRun},
		{"transform error", "renders system", []byte(`{"testResults":[{"name":"` + file + `","status":"failed","message":"Transform failed with 1 error","assertionResults":[]}]}`), BuildFailed},
		{"other file only", "renders system", []byte(`{"testResults":[{"name":"/repo/other.test.ts","status":"passed","assertionResults":[` + a(target, "failed") + `]}]}`), NotRun},
		{"no report", "renders system", nil, BuildFailed},
	}
	for _, c := range cases {
		if got := VitestOutcome(c.report, file, c.test); got != c.want {
			t.Errorf("%s: VitestOutcome = %v, want %v", c.name, got, c.want)
		}
	}
}

// Uncommitted edits must survive: a `git checkout` restore would pass on a
// clean file and lose this one.
func TestMutateRestore_ExactBytesWithUncommittedContent(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	path := filepath.Join(root, "pkg", "calc.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "base")

	// CRLF, no trailing newline, and a non-UTF-8 byte: exact bytes, not text.
	original := []byte("uncommitted edit\r\nreturn a + b\xff")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}

	mutated := bytes.Replace(original, []byte("a + b"), []byte("a - b"), 1)
	m, err := Mutate(root, "pkg/calc.go", original, mutated)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, mutated) {
		t.Fatalf("file during mutation = %q, want %q", got, mutated)
	}
	if got, err := os.ReadFile(backupPath(root, "pkg/calc.go")); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("backup = %q, %v; want the original bytes on disk before the run", got, err)
	}

	if err := m.Restore(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, original) {
		t.Fatalf("restored = %q, want %q", got, original)
	}
	if _, err := os.Stat(backupPath(root, "pkg/calc.go")); !os.IsNotExist(err) {
		t.Errorf("backup still present after restore: %v", err)
	}
	if err := m.Restore(); err != nil {
		t.Errorf("second Restore = %v, want idempotent nil", err)
	}
}

func TestRecover_RestoresStaleBackup(t *testing.T) {
	root := t.TempDir()
	rel := "frontend/app/src/lib/route.ts"
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("MUTATED"), 0o644); err != nil {
		t.Fatal(err)
	}
	backup := backupPath(root, rel)
	if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	restored, err := Recover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 || restored[0] != rel {
		t.Errorf("restored = %v, want [%s]", restored, rel)
	}
	if got, _ := os.ReadFile(path); string(got) != "original\n" {
		t.Errorf("file = %q, want the backup's bytes", got)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Errorf("backup still present: %v", err)
	}

	if restored, err := Recover(t.TempDir()); err != nil || len(restored) != 0 {
		t.Errorf("Recover with no backup dir = %v, %v; want nothing", restored, err)
	}
}

func TestReplay(t *testing.T) {
	const orig = "func Add(a, b int) int { return a + b }\n"
	setup := func(t *testing.T) (string, Row) {
		root := t.TempDir()
		for name, body := range map[string]string{"calc.go": orig, "calc_test.go": "package calc\n"} {
			if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return root, Row{AC: "AC-1", File: "calc.go", Find: "a + b", Replace: "a - b", TestFile: "calc_test.go", Test: "TestAdd"}
	}
	// runner answers by reading the file, so it proves which tree each run saw.
	runner := func(root string, onOrig, onMutated Outcome) Runner {
		return func(Row, Kind) (Outcome, error) {
			b, _ := os.ReadFile(filepath.Join(root, "calc.go"))
			if string(b) == orig {
				return onOrig, nil
			}
			return onMutated, nil
		}
	}

	cases := []struct {
		name              string
		onOrig, onMutated Outcome
		want              Verdict
		reason            string
	}{
		{"proven", Passed, Failed, Proven, "test failed under mutation"},
		{"stays green", Passed, Passed, NotProven, "test stayed green under mutation"},
		{"compile error", Passed, BuildFailed, NotProven, "mutation broke the build, not the test"},
		{"test not run", Passed, NotRun, NotProven, "mutation broke the build, not the test"},
		{"control red", Failed, Failed, Invalid, "control not green: test fails"},
		{"filter matched nothing", NotRun, Failed, Invalid, "control not green: filter matched nothing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, row := setup(t)
			res, err := Replay(root, row, runner(root, c.onOrig, c.onMutated))
			if err != nil {
				t.Fatal(err)
			}
			if res.Verdict != c.want || !strings.Contains(res.Reason, c.reason) {
				t.Errorf("Replay = %+v, want %s containing %q", res, c.want, c.reason)
			}
			if b, _ := os.ReadFile(filepath.Join(root, "calc.go")); string(b) != orig {
				t.Errorf("file not restored: %q", b)
			}
			if entries, _ := os.ReadDir(filepath.Join(root, BackupDir)); len(entries) != 0 {
				t.Errorf("backups left behind: %d", len(entries))
			}
		})
	}

	t.Run("invalid row never runs a test", func(t *testing.T) {
		root, row := setup(t)
		row.Find = "a * b"
		ran := false
		res, err := Replay(root, row, func(Row, Kind) (Outcome, error) { ran = true; return Passed, nil })
		if err != nil || res.Verdict != Invalid || ran {
			t.Errorf("Replay = %+v, %v, ran=%v; want INVALID without a run", res, err, ran)
		}
	})
}
