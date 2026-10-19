// advisories_script_test.go runs scripts/ci/advisories.sh with shimmed curl and go.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const fakeOSVBytes = "fake-osv-scanner"

var (
	advisoriesVersionRE = regexp.MustCompile(`(?m)^version=v\d+\.\d+\.\d+$`)
	advisoriesSHARE     = regexp.MustCompile(`(?m)^sha256=[0-9a-f]{64}`)
)

type advisoriesRun struct {
	shims, tmp string
	stdout     string
	stderr     string
	code       int
}

func (r advisoriesRun) argv(t *testing.T, name string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.shims, name))
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b), true
}

// runAdvisories runs script with shim curl and go first on PATH.
func runAdvisories(t *testing.T, script string, curlFail bool, goExit string) advisoriesRun {
	t.Helper()
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("sha256sum not on PATH")
	}
	shims, tmp := t.TempDir(), t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(shims, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("curl", `printf '%s\n' "$@" >> "`+shims+`/curl.argv"
[ -n "$FAKE_CURL_FAIL" ] && exit 22
while [ $# -gt 0 ]; do
  if [ "$1" = -o ]; then printf '%s' `+fakeOSVBytes+` > "$2"; fi
  shift
done
`)
	write("go", `printf '%s\n' "$@" > "`+shims+`/go.argv"
exit "${FAKE_GO_EXIT:-0}"
`)
	fail := ""
	if curlFail {
		fail = "1"
	}
	out, errOut, code := runBashScript(t,
		`export PATH="$1:$PATH" RUNNER_TEMP="$2"; FAKE_CURL_FAIL="$4" FAKE_GO_EXIT="$5" bash "$3"`,
		shims, tmp, script, fail, goExit)
	return advisoriesRun{shims, tmp, out, errOut, code}
}

// verifiedScript copies the script with its digest set to that of the shim bytes.
func verifiedScript(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "ci", "advisories.sh"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(fakeOSVBytes))
	body := advisoriesSHARE.ReplaceAllString(string(raw), "sha256="+hex.EncodeToString(sum[:]))
	if body == string(raw) {
		t.Fatal("no sha256= line rewritten")
	}
	path := filepath.Join(t.TempDir(), "advisories.sh")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func realAdvisoriesScript(t *testing.T) string {
	return filepath.Join(repoRoot(t), "scripts", "ci", "advisories.sh")
}

func TestAdvisoriesScript_TamperedDownloadDoesNotRunGate(t *testing.T) {
	t.Parallel()
	r := runAdvisories(t, realAdvisoriesScript(t), false, "")
	if r.code == 0 {
		t.Errorf("exit 0 on a digest mismatch\n%s%s", r.stdout, r.stderr)
	}
	if !strings.Contains(r.stdout+r.stderr, "FAILED") {
		t.Errorf("output lacks FAILED:\n%s%s", r.stdout, r.stderr)
	}
	if _, ran := r.argv(t, "go.argv"); ran {
		t.Error("the gate ran after a digest mismatch")
	}
	if fi, err := os.Stat(filepath.Join(r.tmp, "osv-scanner")); err != nil {
		t.Errorf("downloaded binary: %v", err)
	} else if fi.Mode()&0o111 != 0 {
		t.Errorf("unverified binary is executable: %v", fi.Mode())
	}
}

func TestAdvisoriesScript_FailedDownloadDoesNotRunGate(t *testing.T) {
	t.Parallel()
	r := runAdvisories(t, verifiedScript(t), true, "")
	if r.code == 0 {
		t.Errorf("exit 0 on a failed download\n%s%s", r.stdout, r.stderr)
	}
	if _, ran := r.argv(t, "go.argv"); ran {
		t.Error("the gate ran after a failed download")
	}
}

func TestAdvisoriesScript_VerifiedDownloadRunsGate(t *testing.T) {
	t.Parallel()
	r := runAdvisories(t, verifiedScript(t), false, "")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	goArgv, _ := r.argv(t, "go.argv")
	bin := filepath.Join(r.tmp, "osv-scanner")
	if want := "run\n./internal/tools/advisorygate\n-osv\n" + bin + "\n"; goArgv != want {
		t.Errorf("go argv = %q, want %q", goArgv, want)
	}
	if fi, err := os.Stat(bin); err != nil || fi.Mode()&0o111 == 0 {
		t.Errorf("verified binary is not executable: %v %v", fi, err)
	}
	curlArgv, _ := r.argv(t, "curl.argv")
	hit := false
	for _, l := range strings.Split(curlArgv, "\n") {
		if strings.HasPrefix(l, "https://github.com/google/osv-scanner/releases/download/") && strings.HasSuffix(l, "osv-scanner_linux_amd64") {
			hit = true
		}
	}
	if !hit {
		t.Errorf("curl argv has no release URL:\n%s", curlArgv)
	}
}

func TestAdvisoriesScript_GateExitCodePropagates(t *testing.T) {
	t.Parallel()
	if r := runAdvisories(t, verifiedScript(t), false, "3"); r.code != 3 {
		t.Errorf("exit %d, want 3\n%s%s", r.code, r.stdout, r.stderr)
	}
}

func TestAdvisoriesScript_PinShape(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(realAdvisoriesScript(t))
	if err != nil {
		t.Fatal(err)
	}
	for name, re := range map[string]*regexp.Regexp{"version": advisoriesVersionRE, "sha256": advisoriesSHARE} {
		if n := len(re.FindAll(raw, -1)); n != 1 {
			t.Errorf("%s pin matches %d times, want 1", name, n)
		}
	}
}
