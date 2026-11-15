package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const passedOut = "============ test session starts ============\ntests/test_x.py::test_a PASSED [100%]\n"
const failedOut = "============ test session starts ============\ntests/test_x.py::test_a FAILED [100%]\n"

type call struct {
	dir  string
	args []string
}

func (c call) verb() string {
	if c.args[0] == "image" {
		return "image " + c.args[1]
	}
	return c.args[0]
}

func (c call) has(arg string) bool {
	for _, a := range c.args {
		if a == arg {
			return true
		}
	}
	return false
}

type reply struct {
	out  string
	code int
	err  error
}

// fakeDocker answers each docker verb; a nil handler gives the green default.
type fakeDocker struct {
	calls                []call
	version, build, run  func(n int, c call) reply
	rm, inspect, rmLive  func(c call) reply
	onRun                func(c call)
	builds, runs, probes int
}

func (f *fakeDocker) exec(dir, name string, args ...string) (string, int, error) {
	c := call{dir, args}
	f.calls = append(f.calls, c)
	var r reply
	switch c.verb() {
	case "version":
		f.probes++
		r = reply{out: "24.0.7\n"}
		if f.version != nil {
			r = f.version(f.probes, c)
		}
	case "build":
		f.builds++
		r = reply{out: "sha256:clean\n"}
		if f.build != nil {
			r = f.build(f.builds, c)
		}
	case "run":
		f.runs++
		if f.onRun != nil {
			f.onRun(c)
		}
		r = reply{out: passedOut}
		if f.run != nil {
			r = f.run(f.runs, c)
		}
	case "image inspect":
		r = reply{out: "0\n"}
		if f.inspect != nil {
			r = f.inspect(c)
		}
	case "rm":
		if f.rmLive != nil {
			r = f.rmLive(c)
		}
	case "image rm":
		if f.rm != nil {
			r = f.rm(c)
		}
	}
	return r.out, r.code, r.err
}

func (f *fakeDocker) last() call { return f.calls[len(f.calls)-1] }

func (f *fakeDocker) count(verb string) int {
	n := 0
	for _, c := range f.calls {
		if c.verb() == verb {
			n++
		}
	}
	return n
}

func newDocker(root string, f *fakeDocker) *Docker { return &Docker{Exec: f.exec, Root: root} }

func pyRow(file string) Row {
	return Row{AC: "AC-1", File: file, Find: "x", Replace: "y", TestFile: "sidecar/docling/tests/test_x.py", Test: "test_a"}
}

// tree writes the row's file and test file under a temp root.
func tree(t *testing.T, file, content string) string {
	t.Helper()
	root := t.TempDir()
	for f, c := range map[string]string{file: content, "sidecar/docling/tests/test_x.py": "def test_a(): pass\n"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, f), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func assertRestored(t *testing.T, root, file, want string) {
	t.Helper()
	if got, _ := os.ReadFile(filepath.Join(root, file)); string(got) != want {
		t.Errorf("%s = %q, want %q", file, got, want)
	}
	if entries, _ := os.ReadDir(filepath.Join(root, BackupDir)); len(entries) != 0 {
		t.Errorf("backup dir holds %d entries", len(entries))
	}
}

func TestBuildInput(t *testing.T) {
	for _, f := range []string{"Dockerfile", "requirements.txt", "requirements-dev.txt", "requirements.lock", "requirements-dev.lock", "torch.lock", "Dockerfile.dockerignore",
		"fetch_rapidocr_models.py", "pip_check_guard.py", "pyproject.toml", "build.txt"} {
		if !BuildInput("sidecar/docling/" + f) {
			t.Errorf("BuildInput(%s) = false, want true", f)
		}
	}
	for _, f := range []string{"sidecar/docling/tests/test_pins.py", "sidecar/docling/tests/data/x.json",
		"sidecar/other/requirements.txt", ".github/workflows/ci.yml", "Dockerfile", "Dockerfile.dockerignore",
		"sidecar/docling/tests/requirements.txt", "sidecar/docling/sub/Dockerfile", "sidecar/docling/sub/x.lock"} {
		if BuildInput(f) {
			t.Errorf("BuildInput(%s) = true, want false", f)
		}
	}
}

func TestDocker_RunArgs(t *testing.T) {
	root := t.TempDir()
	f := &fakeDocker{}
	d := newDocker(root, f)
	files := map[string]string{
		"sidecar/docling/pip_check_guard.py": "/app/pip_check_guard.py",
		"sidecar/docling/tests/data/x.json":  "/app/tests/data/x.json",
		"sidecar/docling/requirements.lock":  "",
		".github/workflows/ci.yml":           "",
	}
	for file, mount := range files {
		f.calls = nil
		if _, err := d.Pytest(pyRow(file), false); err != nil {
			t.Fatal(err)
		}
		var run call
		for _, c := range f.calls {
			if c.verb() == "run" {
				run = c
			}
		}
		if len(run.args) < 4 || run.args[2] != "--name" || !strings.HasPrefix(run.args[3], "mutationreplay-") {
			t.Fatalf("%s: run args = %q, want --name mutationreplay-*", file, run.args)
		}
		want := []string{"run", "--rm", "--name", run.args[3], "-v", root + ":/repo:ro", "-e", "REPO_ROOT=/repo"}
		if mount != "" {
			want = append(want, "-v", filepath.Join(root, file)+":"+mount+":ro")
		}
		want = append(want, "sha256:clean", "timeout", "600", "pytest", "-v", "tests/test_x.py::test_a")
		if strings.Join(run.args, "\x00") != strings.Join(want, "\x00") {
			t.Errorf("%s: run args = %q, want %q", file, run.args, want)
		}
		if run.dir != root {
			t.Errorf("%s: dir = %q, want %q", file, run.dir, root)
		}
	}
}

func TestDocker_CleanImageBuiltOnceFromTheCleanTree(t *testing.T) {
	root := tree(t, "sidecar/docling/pip_check_guard.py", "original")
	var seen string
	f := &fakeDocker{build: func(_ int, c call) reply {
		b, _ := os.ReadFile(filepath.Join(root, "sidecar/docling/pip_check_guard.py"))
		seen = string(b)
		return reply{out: "sha256:clean\n"}
	}}
	d := newDocker(root, f)
	r1, r2 := pyRow("sidecar/docling/pip_check_guard.py"), pyRow("sidecar/docling/pip_check_guard.py")
	r2.Test = "test_b"
	for _, r := range []Row{r1, r2} {
		if _, err := d.Pytest(r, false); err != nil {
			t.Fatal(err)
		}
	}
	if f.count("version") != 1 || f.count("build") != 1 {
		t.Errorf("version=%d build=%d, want 1 and 1", f.count("version"), f.count("build"))
	}
	for _, c := range f.calls {
		if c.verb() == "build" && strings.Join(c.args, " ") != "build -q --target test -f sidecar/docling/Dockerfile -t docling:test ." {
			t.Errorf("build args = %q", c.args)
		}
		if c.verb() == "run" && !c.has("sha256:clean") {
			t.Errorf("run does not name the printed ID: %q", c.args)
		}
	}
	if seen != "original" {
		t.Errorf("build saw %q, want the original bytes", seen)
	}
}

func TestDocker_RebuildsOnlyWhenTheMutatedMountRunStaysGreen(t *testing.T) {
	for _, mutated := range []bool{false, true} {
		for _, input := range []bool{false, true} {
			for _, mountOut := range []string{passedOut, failedOut} {
				file := "sidecar/docling/tests/data/x.json"
				if input {
					file = "sidecar/docling/requirements.lock"
				}
				f := &fakeDocker{
					build: func(n int, c call) reply {
						if n == 1 {
							return reply{out: "sha256:clean\n"}
						}
						return reply{out: "sha256:new\n"}
					},
					run: func(n int, c call) reply {
						if n == 1 {
							return reply{out: mountOut}
						}
						return reply{out: failedOut}
					},
				}
				out, err := newDocker(t.TempDir(), f).Pytest(pyRow(file), mutated)
				if err != nil {
					t.Fatal(err)
				}
				rebuild := mutated && input && mountOut == passedOut
				if got := f.builds == 2; got != rebuild {
					t.Errorf("mutated=%v input=%v mount=%.20q: rebuilt=%v, want %v", mutated, input, mountOut, got, rebuild)
				}
				if !rebuild {
					if want := PytestOutcome(mountOut, "test_a"); out != want || f.count("image rm") != 0 {
						t.Errorf("no rebuild: out=%v rm=%d", out, f.count("image rm"))
					}
					continue
				}
				if f.calls[3].has("-t") || f.calls[3].verb() != "build" {
					t.Errorf("second build = %q, want no -t", f.calls[3].args)
				}
				if !f.calls[4].has("sha256:new") || f.last().verb() != "image rm" || !f.last().has("sha256:new") {
					t.Errorf("calls = %v, want a run on sha256:new then image rm sha256:new", f.calls)
				}
				if out != Failed {
					t.Errorf("outcome = %v, want the second run's Failed", out)
				}
			}
		}
	}
}

func TestDocker_RebuildSeesTheMutatedTree(t *testing.T) {
	const file = "sidecar/docling/requirements.lock"
	root := tree(t, file, "pillow==13\n")
	var seen []string
	f := &fakeDocker{
		build: func(n int, c call) reply {
			b, _ := os.ReadFile(filepath.Join(root, file))
			seen = append(seen, string(b))
			if n == 1 {
				return reply{out: "sha256:clean\n"}
			}
			return reply{out: "sha256:new\n"}
		},
		run: func(n int, c call) reply {
			if n == 3 {
				return reply{out: failedOut}
			}
			return reply{out: passedOut}
		},
	}
	control, run := Runners(nil, newDocker(root, f))
	r := pyRow(file)
	r.Find, r.Replace = "13", "12"
	res, err := Replay(root, r, control, run)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "pillow==13\n" || seen[1] != "pillow==12\n" {
		t.Errorf("builds saw %q, want the original then the mutated bytes", seen)
	}
	if res.Verdict != Proven {
		t.Errorf("verdict = %v (%s), want PROVEN", res.Verdict, res.Reason)
	}
	assertRestored(t, root, file, "pillow==13\n")
}

func TestDocker_KeepsTheCleanImageWhenTheRebuildIDMatches(t *testing.T) {
	f := &fakeDocker{}
	if _, err := newDocker(t.TempDir(), f).Pytest(pyRow("sidecar/docling/requirements.lock"), true); err != nil {
		t.Fatal(err)
	}
	if f.builds != 2 || f.count("image rm") != 0 {
		t.Errorf("builds=%d image rm=%d, want 2 and 0", f.builds, f.count("image rm"))
	}
}

func rebuildFake(second reply) *fakeDocker {
	return &fakeDocker{
		build: func(n int, c call) reply {
			if n == 1 {
				return reply{out: "sha256:clean\n"}
			}
			return reply{out: "sha256:new\n"}
		},
		run: func(n int, c call) reply {
			if n == 1 {
				return reply{out: passedOut}
			}
			return second
		},
	}
}

func TestDocker_RemovesTheMutatedImageOnEveryPath(t *testing.T) {
	for name, second := range map[string]reply{
		"exit 125":    {out: "docker: invalid mount config", code: 125},
		"exit 124":    {out: "test session starts\n", code: 124},
		"start error": {err: errors.New("fork/exec docker: no such file")},
	} {
		f := rebuildFake(second)
		if _, err := newDocker(t.TempDir(), f).Pytest(pyRow("sidecar/docling/requirements.lock"), true); err == nil {
			t.Errorf("%s: want an error", name)
		}
		if l := f.last(); l.verb() != "image rm" || !l.has("sha256:new") {
			t.Errorf("%s: last call = %q, want image rm sha256:new", name, l.args)
		}
	}
}

func TestDocker_ImageRmFailureKeepsTheOutcome(t *testing.T) {
	f := rebuildFake(reply{out: failedOut})
	f.rm = func(call) reply { return reply{out: "Error response from daemon: conflict", code: 1} }
	rd, wr, _ := os.Pipe()
	stderr := os.Stderr
	os.Stderr = wr
	out, err := newDocker(t.TempDir(), f).Pytest(pyRow("sidecar/docling/requirements.lock"), true)
	os.Stderr = stderr
	wr.Close()
	warn, _ := io.ReadAll(rd)
	if want := "mutationreplay: WARNING image rm sha256:new: Error response from daemon: conflict"; !strings.Contains(string(warn), want) {
		t.Errorf("stderr = %q, want %q", warn, want)
	}
	if out != Failed || err != nil {
		t.Errorf("= %v, %v; want Failed, nil", out, err)
	}
	if f.count("image rm") != 1 {
		t.Errorf("image rm called %d times, want 1", f.count("image rm"))
	}
}

func TestReplay_FailedMutatedBuildIsInvalidAndRestores(t *testing.T) {
	const file = "sidecar/docling/requirements.lock"
	root := tree(t, file, "x\n")
	f := &fakeDocker{build: func(n int, c call) reply {
		if n == 1 {
			return reply{out: "sha256:clean\n"}
		}
		return reply{out: "ERROR: failed to solve", code: 1}
	}}
	control, run := Runners(nil, newDocker(root, f))
	res, err := Replay(root, pyRow(file), control, run)
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != Invalid || !strings.Contains(res.Reason, "mutated run: image build from the mutated tree failed") || !strings.Contains(res.Reason, "failed to solve") {
		t.Errorf("result = %+v", res)
	}
	assertRestored(t, root, file, "x\n")
	if f.count("image rm") != 0 {
		t.Errorf("image rm called %d times after a failed build, want 0", f.count("image rm"))
	}
}

func TestReplay_PytestRowRestoresTheTreeOnEveryPath(t *testing.T) {
	const py, lock = "sidecar/docling/pip_check_guard.py", "sidecar/docling/requirements.lock"
	collect := "test session starts\ncollected 0 items / 1 error\n_____ ERROR collecting tests/test_x.py _____\n"
	cases := []struct {
		name, file string
		second     reply
		want       Verdict
	}{
		{"passed", py, reply{out: passedOut}, NotProven},
		{"failed", py, reply{out: failedOut, code: 1}, Proven},
		{"collection error", py, reply{out: collect, code: 2}, NotProven},
		{"conftest import error", py, reply{out: "ImportError while loading conftest '/app/tests/conftest.py'.\n", code: 4}, NotProven},
		{"exit 125", py, reply{out: "docker: invalid mount config", code: 125}, Invalid},
		{"exit 124", py, reply{out: "test session starts\n", code: 124}, Invalid},
		{"exec timeout", py, reply{err: errors.New("timed out after 30m0s")}, Invalid},
		{"rebuild passed", lock, reply{out: passedOut}, NotProven},
		{"rebuild run exit 125", lock, reply{out: "docker: invalid mount config", code: 125}, Invalid},
	}
	for _, c := range cases {
		root := tree(t, c.file, "x\n")
		f := rebuildFake(reply{out: passedOut})
		f.run = func(n int, call call) reply {
			if n == 1 || n == 2 && c.file == lock {
				return reply{out: passedOut}
			}
			return c.second
		}
		control, run := Runners(nil, newDocker(root, f))
		res, err := Replay(root, pyRow(c.file), control, run)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if res.Verdict != c.want {
			t.Errorf("%s: verdict = %v (%s), want %v", c.name, res.Verdict, res.Reason, c.want)
		}
		assertRestored(t, root, c.file, "x\n")
	}
}

func TestRunners_NoDockerKeepsGoRowsReplaying(t *testing.T) {
	root := tree(t, "sidecar/docling/pip_check_guard.py", "x\n")
	for f, c := range map[string]string{"pkg/a.go": "x\n", "pkg/a_test.go": "package pkg\n"} {
		os.MkdirAll(filepath.Dir(filepath.Join(root, f)), 0o755)
		os.WriteFile(filepath.Join(root, f), []byte(c), 0o644)
	}
	f := &fakeDocker{version: func(int, call) reply {
		return reply{out: "failed to connect to the docker API", code: 1}
	}}
	calls := 0
	other := func(Row, Kind) (Outcome, error) {
		calls++
		if calls == 1 {
			return Passed, nil
		}
		return Failed, nil
	}
	control, run := Runners(other, newDocker(root, f))
	goRow := Row{AC: "AC-1", File: "pkg/a.go", Find: "x", Replace: "y", TestFile: "pkg/a_test.go", Test: "TestA"}
	res, err := Replay(root, goRow, control, run)
	if err != nil || res.Verdict != Proven {
		t.Errorf("go row = %+v, %v; want PROVEN", res, err)
	}
	for _, test := range []string{"test_a", "test_b"} {
		r := pyRow("sidecar/docling/pip_check_guard.py")
		r.Test = test
		res, err := Replay(root, r, control, run)
		if err != nil || res.Verdict != Invalid || !strings.Contains(res.Reason, "Docker is not running or not installed") {
			t.Errorf("pytest row = %+v, %v; want INVALID naming Docker", res, err)
		}
	}
	if f.probes != 1 {
		t.Errorf("version called %d times, want 1", f.probes)
	}
}

func TestDocker_DockerBinaryMissing(t *testing.T) {
	f := &fakeDocker{version: func(int, call) reply {
		return reply{err: errors.New(`exec: "docker": executable file not found in $PATH`)}
	}}
	_, err := newDocker(t.TempDir(), f).Pytest(pyRow("sidecar/docling/pip_check_guard.py"), false)
	if err == nil || !strings.Contains(err.Error(), "Docker is not running or not installed") || !strings.Contains(err.Error(), "executable file not found") {
		t.Errorf("err = %v", err)
	}
}

func TestDocker_CleanBuildFailureIsKept(t *testing.T) {
	f := &fakeDocker{build: func(int, call) reply { return reply{out: "ERROR: boom", code: 1} }}
	d := newDocker(t.TempDir(), f)
	for _, test := range []string{"test_a", "test_b"} {
		r := pyRow("sidecar/docling/pip_check_guard.py")
		r.Test = test
		if _, err := d.Pytest(r, false); err == nil || !strings.Contains(err.Error(), "docling:test build failed") {
			t.Errorf("%s: err = %v", test, err)
		}
	}
	if f.builds != 1 {
		t.Errorf("build called %d times, want 1", f.builds)
	}
}

func TestDocker_BuildExitingNonZeroIsFailedEvenWithAnID(t *testing.T) {
	f := &fakeDocker{build: func(int, call) reply { return reply{out: "sha256:clean\n", code: 1} }}
	_, err := newDocker(t.TempDir(), f).Pytest(pyRow("sidecar/docling/pip_check_guard.py"), false)
	if err == nil || !strings.Contains(err.Error(), "docling:test build failed") {
		t.Errorf("clean build: err = %v", err)
	}
	f = rebuildFake(reply{out: passedOut})
	build := f.build
	f.build = func(n int, c call) reply {
		if r := build(n, c); n == 1 {
			return r
		}
		return reply{out: "sha256:new\n", code: 1}
	}
	_, err = newDocker(t.TempDir(), f).Pytest(pyRow("sidecar/docling/requirements.lock"), true)
	if err == nil || !strings.Contains(err.Error(), "image build from the mutated tree failed") || f.runs != 1 {
		t.Errorf("mutated build: err = %v, runs = %d", err, f.runs)
	}
}

func TestDocker_BuildWithoutAnImageID(t *testing.T) {
	f := &fakeDocker{build: func(int, call) reply { return reply{out: "done\n"} }}
	_, err := newDocker(t.TempDir(), f).Pytest(pyRow("sidecar/docling/pip_check_guard.py"), false)
	if err == nil || !strings.Contains(err.Error(), "docling:test build failed") {
		t.Errorf("err = %v", err)
	}
}

func TestDocker_RunFailureIsAnError(t *testing.T) {
	row := pyRow("sidecar/docling/pip_check_guard.py")
	run := func(r reply) (Outcome, error) {
		return newDocker(t.TempDir(), &fakeDocker{run: func(int, call) reply { return r }}).Pytest(row, false)
	}
	if _, err := run(reply{out: "docker: invalid mount config", code: 125}); err == nil || !strings.Contains(err.Error(), "docker run failed (exit 125)") {
		t.Errorf("exit 125: err = %v", err)
	}
	if _, err := run(reply{out: "oops", code: 1}); err == nil || !strings.Contains(err.Error(), "(exit 1)") {
		t.Errorf("exit 1: err = %v", err)
	}
	if out, err := run(reply{out: failedOut, code: 1}); out != Failed || err != nil {
		t.Errorf("failed test = %v, %v; want Failed, nil", out, err)
	}
	if out, err := run(reply{out: "ImportError while loading conftest '/app/tests/conftest.py'.\n", code: 4}); out != BuildFailed || err != nil {
		t.Errorf("conftest = %v, %v; want BuildFailed, nil", out, err)
	}
	if _, err := run(reply{out: "test session starts\n", code: 124}); err == nil || !strings.Contains(err.Error(), "pytest timed out after 600s") {
		t.Errorf("exit 124: err = %v", err)
	}
}

func TestDocker_TimeoutIsInvalid(t *testing.T) {
	const file = "sidecar/docling/pip_check_guard.py"
	root := tree(t, file, "x\n")
	f := &fakeDocker{build: func(int, call) reply { return reply{err: errors.New("timed out after 30m0s")} }}
	control, run := Runners(nil, newDocker(root, f))
	res, err := Replay(root, pyRow(file), control, run)
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != Invalid || !strings.Contains(res.Reason, "control run: docker build timed out after 30m0s") {
		t.Errorf("result = %+v", res)
	}
	assertRestored(t, root, file, "x\n")
}

func TestDocker_MutatedBuildTimeoutIsInvalid(t *testing.T) {
	const file = "sidecar/docling/requirements.lock"
	root := tree(t, file, "x\n")
	f := &fakeDocker{build: func(n int, c call) reply {
		if n == 1 {
			return reply{out: "sha256:clean\n"}
		}
		return reply{err: errors.New("timed out after 30m0s")}
	}}
	control, run := Runners(nil, newDocker(root, f))
	res, err := Replay(root, pyRow(file), control, run)
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != Invalid || !strings.Contains(res.Reason, "mutated run: docker build timed out after 30m0s") {
		t.Errorf("result = %+v", res)
	}
	assertRestored(t, root, file, "x\n")
}

func TestDocker_BuildTimeScriptRowRebuildsOnlyOnAGreenMount(t *testing.T) {
	const fetch = "sidecar/docling/fetch_rapidocr_models.py"
	for _, tc := range []struct {
		file, mount string
		rebuild     bool
	}{
		{fetch, passedOut, true},
		{fetch, failedOut, false},
		{"sidecar/docling/tests/test_x.py", passedOut, false},
	} {
		f := rebuildFake(reply{out: failedOut})
		f.run = func(n int, c call) reply {
			if n == 1 {
				return reply{out: tc.mount}
			}
			return reply{out: failedOut}
		}
		if _, err := newDocker(t.TempDir(), f).Pytest(pyRow(tc.file), true); err != nil {
			t.Fatal(err)
		}
		if got := f.builds == 2; got != tc.rebuild {
			t.Errorf("%s mount=%.20q: rebuilt=%v, want %v", tc.file, tc.mount, got, tc.rebuild)
		}
		var firstRun call
		for _, c := range f.calls {
			if c.verb() == "run" {
				firstRun = c
				break
			}
		}
		if !firstRun.has(filepath.Join(firstRun.dir, tc.file) + ":/app/" + strings.TrimPrefix(tc.file, "sidecar/docling/") + ":ro") {
			t.Errorf("%s: /app mount missing from %q", tc.file, firstRun.args)
		}
	}
}

func TestDocker_RunIsNamedAndRemovedWhenItTimesOut(t *testing.T) {
	f := &fakeDocker{run: func(int, call) reply { return reply{err: errors.New("timed out after 30m0s")} }}
	if _, err := newDocker(t.TempDir(), f).Pytest(pyRow("sidecar/docling/tests/test_x.py"), false); err == nil {
		t.Fatal("want an error")
	}
	var name string
	for i, a := range f.calls[len(f.calls)-2].args {
		if a == "--name" {
			name = f.calls[len(f.calls)-2].args[i+1]
		}
	}
	rm := f.last()
	if name == "" || rm.verb() != "rm" || !rm.has("-f") || !rm.has(name) {
		t.Errorf("calls = %v, want docker rm -f %q after the run", f.calls, name)
	}
}

func TestDocker_RemoveLiveRemovesTheRunInFlight(t *testing.T) {
	f := &fakeDocker{}
	d := newDocker(t.TempDir(), f)
	d.RemoveLive()
	if f.count("rm") != 0 {
		t.Fatal("rm -f with no run in flight")
	}
	f.onRun = func(call) { d.RemoveLive() }
	if _, err := d.Pytest(pyRow("sidecar/docling/tests/test_x.py"), false); err != nil {
		t.Fatal(err)
	}
	var name string
	for _, c := range f.calls {
		if c.verb() == "run" {
			name = c.args[3]
		}
	}
	if f.count("rm") != 1 || !f.last().has(name) {
		t.Errorf("calls = %v, want one rm -f %s", f.calls, name)
	}
	f.calls = nil
	d.RemoveLive()
	if f.count("rm") != 0 {
		t.Error("live name kept after the run ended")
	}
}

func TestDocker_KeepsATaggedRebuildImage(t *testing.T) {
	for name, inspect := range map[string]reply{
		"tagged":         {out: "1\n"},
		"inspect fails":  {out: "Error: No such image", code: 1},
		"inspect errors": {err: errors.New("fork/exec docker: no such file")},
	} {
		f := rebuildFake(reply{out: failedOut})
		f.inspect = func(call) reply { return inspect }
		if _, err := newDocker(t.TempDir(), f).Pytest(pyRow("sidecar/docling/requirements.lock"), true); err != nil {
			t.Fatal(err)
		}
		if f.count("image inspect") != 1 || f.count("image rm") != 0 {
			t.Errorf("%s: inspect=%d rm=%d, want 1 and 0", name, f.count("image inspect"), f.count("image rm"))
		}
	}
}

func TestDocker_RemoveLiveWarnsOnlyOnARealFailure(t *testing.T) {
	for name, tc := range map[string]struct {
		rm   reply
		want string
	}{
		"already in progress": {reply{out: "Error response from daemon: removal of container x is already in progress", code: 1}, ""},
		"no such container":   {reply{out: "Error response from daemon: No such container: x", code: 1}, ""},
		"daemon error":        {reply{out: "Error response from daemon: boom", code: 1}, "WARNING rm -f mutationreplay-"},
		"exec error":          {reply{err: errors.New("fork/exec docker: gone")}, "gone"},
	} {
		f := &fakeDocker{rmLive: func(call) reply { return tc.rm }}
		d := newDocker(t.TempDir(), f)
		d.live = "mutationreplay-1-1"
		r, w, _ := os.Pipe()
		old := os.Stderr
		os.Stderr = w
		d.RemoveLive()
		os.Stderr = old
		w.Close()
		got, _ := io.ReadAll(r)
		if tc.want == "" && len(got) != 0 || !strings.Contains(string(got), tc.want) || strings.Contains(string(got), "<nil>") {
			t.Errorf("%s: stderr = %q, want %q", name, got, tc.want)
		}
	}
}
