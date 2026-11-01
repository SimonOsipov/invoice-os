// rls_test_gate_script_test.go runs scripts/ci/rls-test-gate.sh against a logging `go` shim.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const rlsGateShim = `#!/bin/sh
echo "$*" >> "$SHIM_LOG"
case "$1" in
test) cat "$STREAM"; exit "${GO_TEST_RC:-0}" ;;
run)
  case "$2" in
  ./internal/tools/gotestretry) cat "$STREAM" > "$4"; exit "${RETRY_RC:-0}" ;;
  ./internal/tools/rlsgate)
    if [ -n "${GATE_RC-}" ]; then cat > /dev/null; exit "$GATE_RC"; fi
    exec "$REAL_GO" "$@" ;;
  esac ;;
esac
exit 99
`

type rlsGateRun struct {
	stdout string
	exit   int
	argv   []string
	tmp    string
}

// runRLSGate runs the script in the repo root; env entries replace the ambient ones, and NIGHTLY_RESULTS is dropped unless given.
func runRLSGate(t *testing.T, stream string, env map[string]string, args ...string) rlsGateRun {
	t.Helper()
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	streamPath, err := filepath.Abs(filepath.Join("testdata", stream))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin, tmp, logPath := filepath.Join(dir, "bin"), filepath.Join(dir, "tmp"), filepath.Join(dir, "argv.log")
	for _, d := range []string{bin, tmp} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeShim(t, filepath.Join(bin, "go"), rlsGateShim)
	// BSD mktemp ignores TMPDIR; without this shim the TMPDIR-empty check is vacuous on macOS.
	writeShim(t, filepath.Join(bin, "mktemp"), "#!/bin/sh\nif [ $# -eq 0 ]; then exec /usr/bin/mktemp \"$TMPDIR/tmp.XXXXXX\"; fi\nexec /usr/bin/mktemp \"$@\"\n")

	cmd := exec.Command("/bin/bash", append([]string{"scripts/ci/rls-test-gate.sh"}, args...)...)
	cmd.Dir = repoRoot(t)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "NIGHTLY_RESULTS=") && !strings.HasPrefix(kv, "TMPDIR=") && !strings.HasPrefix(kv, "PATH=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "TMPDIR="+tmp,
		"SHIM_LOG="+logPath, "STREAM="+streamPath, "REAL_GO="+realGo)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdout, _, runErr := runToFiles(t, cmd, false)
	code := 0
	if ee, ok := runErr.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if runErr != nil {
		t.Fatal(runErr)
	}
	raw, _ := os.ReadFile(logPath)
	return rlsGateRun{stdout: stdout, exit: code, argv: strings.Split(strings.TrimSpace(string(raw)), "\n"), tmp: tmp}
}

func globOne(t *testing.T, pattern string) []string {
	t.Helper()
	m, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRLSGateScript_UnsetLeavesNoFile(t *testing.T) {
	t.Parallel()
	r := runRLSGate(t, "rls_gate_pass.jsonl", map[string]string{"GATE_RC": "0"}, "-count=1", "./x/...")
	if r.exit != 0 {
		t.Fatalf("exit = %d, want 0\n%s", r.exit, r.stdout)
	}
	if got := strings.Join(r.argv, "|"); got != "test -json -count=1 ./x/...|run ./internal/tools/rlsgate" {
		t.Errorf("argv = %q, want go test -json then rlsgate, no gotestretry", got)
	}
	var found []string
	_ = filepath.WalkDir(filepath.Dir(r.tmp), func(p string, _ os.DirEntry, _ error) error {
		if strings.HasPrefix(filepath.Base(p), "gotest-") {
			found = append(found, p)
		}
		return nil
	})
	if len(found) != 0 {
		t.Errorf("gotest-* files left: %v", found)
	}
	if left, _ := os.ReadDir(r.tmp); len(left) != 0 {
		t.Errorf("TMPDIR not empty after the run: %v", left)
	}
}

func TestRLSGateScript_UnsetExitCodesUnchanged(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ goRC, gateRC, want int }{{0, 0, 0}, {0, 1, 1}, {1, 0, 1}, {1, 1, 1}} {
		t.Run(strconv.Itoa(c.goRC)+"_"+strconv.Itoa(c.gateRC), func(t *testing.T) {
			t.Parallel()
			r := runRLSGate(t, "rls_gate_pass.jsonl",
				map[string]string{"GO_TEST_RC": strconv.Itoa(c.goRC), "GATE_RC": strconv.Itoa(c.gateRC)}, "-count=1", "./x/...")
			if r.exit != c.want {
				t.Errorf("exit = %d, want %d", r.exit, c.want)
			}
		})
	}
}

func TestRLSGateScript_UnsetOutputUnchanged(t *testing.T) {
	t.Parallel()
	want, err := os.ReadFile(filepath.Join("testdata", "rls_gate_unset.golden"))
	if err != nil {
		t.Fatal(err)
	}
	r := runRLSGate(t, "rls_gate_mixed.jsonl", map[string]string{"GO_TEST_RC": "1"}, "-count=1", "./x/...")
	if r.exit != 1 {
		t.Errorf("exit = %d, want 1", r.exit)
	}
	if r.stdout != string(want) {
		t.Errorf("stdout differs from rls_gate_unset.golden:\n got: %q\nwant: %q", r.stdout, want)
	}
}

func TestRLSGateScript_SetUsesRetryAndKeepsStream(t *testing.T) {
	t.Parallel()
	out := filepath.Join(t.TempDir(), "r")
	r := runRLSGate(t, "rls_gate_pass.jsonl", map[string]string{"NIGHTLY_RESULTS": out, "GATE_RC": "0"}, "-count=1", "./x/...")
	if r.exit != 0 {
		t.Fatalf("exit = %d, want 0\n%s", r.exit, r.stdout)
	}
	if len(r.argv) != 2 || !strings.HasPrefix(r.argv[0], "run ./internal/tools/gotestretry -out ") ||
		!strings.HasSuffix(r.argv[0], " -- -count=1 ./x/...") || r.argv[1] != "run ./internal/tools/rlsgate" {
		t.Errorf("argv = %q, want gotestretry -out F -- -count=1 ./x/... then rlsgate", r.argv)
	}
	if all := globOne(t, filepath.Join(out, "gotest-*")); len(all) != 1 || !strings.HasSuffix(all[0], ".json") {
		t.Fatalf("files under %s = %v, want exactly one gotest-*.json", out, all)
	}
	got, _ := os.ReadFile(globOne(t, filepath.Join(out, "gotest-*.json"))[0])
	want, _ := os.ReadFile(filepath.Join("testdata", "rls_gate_pass.jsonl"))
	if string(got) != string(want) {
		t.Errorf("kept stream = %q, want %q", got, want)
	}
}

func TestRLSGateScript_SetExitCodes(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ retryRC, gateRC, want int }{{0, 0, 0}, {0, 1, 1}, {1, 0, 1}, {1, 1, 1}} {
		t.Run(strconv.Itoa(c.retryRC)+"_"+strconv.Itoa(c.gateRC), func(t *testing.T) {
			t.Parallel()
			r := runRLSGate(t, "rls_gate_pass.jsonl", map[string]string{
				"NIGHTLY_RESULTS": filepath.Join(t.TempDir(), "r"),
				"RETRY_RC":        strconv.Itoa(c.retryRC), "GATE_RC": strconv.Itoa(c.gateRC),
			}, "-count=1", "./x/...")
			if r.exit != c.want {
				t.Errorf("exit = %d, want %d", r.exit, c.want)
			}
		})
	}
}

func TestRLSGateScript_TwoCallsTwoFiles(t *testing.T) {
	t.Parallel()
	out := filepath.Join(t.TempDir(), "r")
	env := map[string]string{"NIGHTLY_RESULTS": out, "GATE_RC": "0"}
	runRLSGate(t, "rls_gate_pass.jsonl", env, "-count=1", "./x/...")
	runRLSGate(t, "rls_gate_pass.jsonl", env, "-count=1", "./y/...")
	if got := globOne(t, filepath.Join(out, "gotest-*.json")); len(got) != 2 {
		t.Errorf("gotest-*.json files = %v, want 2 distinct", got)
	}
}

func TestRLSGateScript_CopyFailureKeepsExitCode(t *testing.T) {
	t.Parallel()
	blocker := filepath.Join(t.TempDir(), "file")
	writeFile(t, blocker, "x")
	r := runRLSGate(t, "rls_gate_pass.jsonl",
		map[string]string{"NIGHTLY_RESULTS": blocker, "RETRY_RC": "0", "GATE_RC": "0"}, "-count=1", "./x/...")
	if r.exit != 0 {
		t.Errorf("exit = %d, want 0", r.exit)
	}
	if !strings.Contains(r.stdout, "::warning::") {
		t.Errorf("stdout lacks ::warning:: :\n%s", r.stdout)
	}
}
