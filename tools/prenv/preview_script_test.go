// preview_script_test.go runs scripts/ci/preview.sh against a gcloud shim that logs argv and captures stdin.
package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const previewGcloudShim = `echo "$*" >> "$D/calls"
case "$1 $2 $3" in
"run deploy "*)
  prev=
  for a in "$@"; do
    [ "$prev" = --env-vars-file ] && cp "$a" "$D/env-$3"
    prev=$a
  done ;;
"secrets create "*) cat > "$D/created-$3" ;;
"secrets list "*) cat "$D/have" 2>/dev/null || : ;;
"secrets versions add") cat > "$D/added-$4" ;;
"secrets versions access")
  prev=
  for a in "$@"; do
    [ "$prev" = --secret ] && name=$a
    prev=$a
  done
  cat "$D/access-$name" 2>/dev/null || printf pw ;;
"sql instances describe") echo 10.0.0.1 ;;
esac
`

type previewRun struct {
	dir, stdout, stderr string
	code                int
}

func (r previewRun) file(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (r previewRun) exists(name string) bool {
	_, err := os.Stat(filepath.Join(r.dir, name))
	return err == nil
}

// runPreview runs preview.sh in a scratch dir with shim gcloud and go (go run ./tools/prenv -> the built prenv).
func runPreview(t *testing.T, setup func(dir string), args ...string) previewRun {
	t.Helper()
	dir, shims := t.TempDir(), t.TempDir()
	if setup != nil {
		setup(dir)
	}
	writeShim(t, filepath.Join(shims, "gcloud"), "D='"+dir+"'\n"+previewGcloudShim)
	writeShim(t, filepath.Join(shims, "go"), "shift 2\nexec '"+binPath+"' \"$@\"\n")
	cmd := exec.Command("bash", append([]string{filepath.Join(repoRoot(t), "scripts/ci/preview.sh")}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+shims+string(os.PathListSeparator)+os.Getenv("PATH"))
	stdout, stderr, err := runToFiles(t, cmd, false)
	r := previewRun{dir: dir, stdout: stdout, stderr: stderr}
	if ee, ok := err.(*exec.ExitError); ok {
		r.code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return r
}

func writeIn(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// deployCalls maps service -> the argv of its `run deploy` call.
func deployCalls(t *testing.T, r previewRun, pr string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, line := range strings.Split(r.file(t, "calls"), "\n") {
		f := strings.Fields(line)
		if len(f) > 2 && f[0] == "run" && f[1] == "deploy" {
			out[strings.TrimSuffix(f[2], "-pr-"+pr)] = line
		}
	}
	return out
}

func flagValue(call, flag string) string {
	f := strings.Fields(call)
	for i, a := range f {
		if a == flag && i+1 < len(f) {
			return f[i+1]
		}
	}
	return ""
}

func previewList(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts/ci/preview.sh"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)\b` + name + `=\(([^)]*)\)`).FindSubmatch(b)
	if m == nil {
		t.Fatalf("no %s array in preview.sh", name)
	}
	return strings.Fields(string(m[1]))
}

func budgetFleet(t *testing.T, r previewRun) int {
	t.Helper()
	m := regexp.MustCompile(`(?m)^fleet (\d+)$`).FindStringSubmatch(r.stdout)
	if m == nil {
		t.Fatalf("budget printed no fleet line:\n%s", r.stdout)
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func TestPreviewDeployMaxInstancesByService(t *testing.T) {
	t.Parallel()
	want := map[string]string{"gateway": "3", "auth": "1", "worker": "1", "portfolio": "2", "tenancy": "3", "docling": "3",
		"invoice": "3", "validation": "2", "dashboard": "2", "submission": "2", "notifications": "2",
		"app": "3", "landing": "3", "library": "3", "blog": "3", "ops-console": "3", "support-console": "3"}
	all := append(previewList(t, "BACKENDS"), previewList(t, "SITES")...)
	if len(all) != len(want) {
		t.Fatalf("preview.sh lists %d services, table has %d", len(all), len(want))
	}
	r := runPreview(t, nil, append([]string{"deploy", "7", "abc123"}, all...)...)
	if r.code != 0 {
		t.Fatalf("deploy exited %d: %s", r.code, r.stderr)
	}
	calls := deployCalls(t, r, "7")
	for svc, want := range want {
		if got := flagValue(calls[svc], "--max-instances"); got != want {
			t.Errorf("%s --max-instances = %q, want %s", svc, got, want)
		}
	}
	if got := flagValue(calls["worker"], "--min-instances"); got != "1" {
		t.Errorf("worker --min-instances = %q, want 1", got)
	}
	if got := flagValue(calls["gateway"], "--min-instances"); got != "0" {
		t.Errorf("gateway --min-instances = %q, want 0", got)
	}
}

func TestPreviewDeployEverySiteAndBackendHasAValue(t *testing.T) {
	t.Parallel()
	all := append(previewList(t, "BACKENDS"), previewList(t, "SITES")...)
	r := runPreview(t, nil, append([]string{"deploy", "7", "abc123"}, all...)...)
	if r.code != 0 {
		t.Fatalf("deploy exited %d: %s", r.code, r.stderr)
	}
	calls := deployCalls(t, r, "7")
	for _, svc := range all {
		if v := flagValue(calls[svc], "--max-instances"); v != "1" && v != "2" && v != "3" {
			t.Errorf("%s --max-instances = %q", svc, v)
		}
	}
}

func TestPreviewDeployAuthKeepsTheGoTruePool(t *testing.T) {
	t.Parallel()
	r := runPreview(t, nil, "deploy", "7", "abc123", "auth")
	if !strings.Contains(r.file(t, "env-auth-pr-7"), "GOTRUE_DB_MAX_POOL_SIZE: '3'") {
		t.Fatalf("auth env file lacks the GoTrue pool:\n%s", r.file(t, "env-auth-pr-7"))
	}
}

func TestPreviewGatewayDeploysTheMockKeySecret(t *testing.T) {
	t.Parallel()
	r := runPreview(t, nil, "deploy", "7", "abc123", "gateway")
	got := flagValue(deployCalls(t, r, "7")["gateway"], "--set-secrets")
	if !strings.Contains(got, "GATEWAY_MOCK_ISSUER_KEY=pr-7-mock-issuer-key:latest") {
		t.Fatalf("--set-secrets = %q", got)
	}
}

func TestPreviewBudgetFleetFitsThreePreviews(t *testing.T) {
	t.Parallel()
	r := runPreview(t, nil, "budget")
	if r.code != 0 {
		t.Fatalf("budget exited %d: %s", r.code, r.stderr)
	}
	if n := budgetFleet(t, r); n > 30 || 3*n > 95 {
		t.Fatalf("fleet = %d: want <= 30 and 3 x fleet <= 95", n)
	}
}

func TestPreviewBudgetNeedsNoGcloud(t *testing.T) {
	t.Parallel()
	r := runPreview(t, nil, "budget")
	if r.code != 0 || r.exists("calls") {
		t.Fatalf("budget exit %d, gcloud called: %v", r.code, r.exists("calls"))
	}
}

func TestPreviewSecretsPoolValues(t *testing.T) {
	t.Parallel()
	r := runPreview(t, nil, "secrets", "7")
	if r.code != 0 {
		t.Fatalf("secrets exited %d: %s", r.code, r.stderr)
	}
	for name, want := range map[string]string{"app": "1", "worker": "6", "reader": "1", "gateway": "1"} {
		if dsn := r.file(t, "created-pr-7-database-"+name+"-url"); !strings.HasSuffix(dsn, "&pool_max_conns="+want) {
			t.Errorf("database-%s-url = %q, want pool_max_conns=%s", name, dsn, want)
		}
	}
}

func allPRSecrets(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, n := range previewList(t, "PR_SECRETS") {
		b.WriteString("pr-7-" + n + "\n")
	}
	return b.String()
}

func TestPreviewSecretsAddsAVersionWhenThePoolDiffers(t *testing.T) {
	t.Parallel()
	const dsn = "postgres://u:p@10.0.0.1:5432/pr_7?sslmode=require&pool_max_conns="
	r := runPreview(t, func(dir string) {
		writeIn(t, dir, "have", allPRSecrets(t))
		writeIn(t, dir, "access-pr-7-database-app-url", dsn+"2")
		writeIn(t, dir, "access-pr-7-database-worker-url", dsn+"6")
		writeIn(t, dir, "access-pr-7-database-reader-url", dsn+"1")
		writeIn(t, dir, "access-pr-7-database-gateway-url", dsn+"1")
	}, "secrets", "7")
	if r.code != 0 {
		t.Fatalf("secrets exited %d: %s", r.code, r.stderr)
	}
	if !strings.Contains(r.stdout, "updated pr-7-database-app-url") || strings.Contains(r.stdout+r.stderr, "postgres://") {
		t.Errorf("rollover output must name the secret and never a DSN:\n%s%s", r.stdout, r.stderr)
	}
	if got := r.file(t, "added-pr-7-database-app-url"); got != dsn+"1" {
		t.Errorf("new app version = %q, want %q", got, dsn+"1")
	}
	for _, n := range []string{"worker", "reader", "gateway"} {
		if r.exists("added-pr-7-database-" + n + "-url") {
			t.Errorf("database-%s-url already matched but got a new version", n)
		}
	}
	if strings.Contains(r.file(t, "calls"), "secrets create") {
		t.Error("an existing secret was created again")
	}
}

func TestPreviewSecretsMakesAValidMockKey(t *testing.T) {
	t.Parallel()
	r := runPreview(t, nil, "secrets", "7")
	cmd := exec.Command(binPath, "jwk-check")
	cmd.Stdin = strings.NewReader(r.file(t, "created-pr-7-mock-issuer-key"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("jwk-check rejected the mock key: %v\n%s", err, out)
	}
	if !strings.Contains(r.stdout, "created pr-7-mock-issuer-key\n") {
		t.Errorf("stdout lacks the created line:\n%s", r.stdout)
	}
}

func TestPreviewSecretsNeverPrintsTheKey(t *testing.T) {
	t.Parallel()
	r := runPreview(t, nil, "secrets", "7")
	var keys []map[string]any
	if err := json.Unmarshal([]byte(r.file(t, "created-pr-7-mock-issuer-key")), &keys); err != nil || len(keys) != 1 {
		t.Fatalf("mock key is not a one-key JWK array: %v", err)
	}
	d, _ := keys[0]["d"].(string)
	if d == "" {
		t.Fatal("mock key has no d member")
	}
	if strings.Contains(r.stdout, d) || strings.Contains(r.stderr, d) {
		t.Fatal("the key's d member reached stdout or stderr")
	}
	if strings.Contains(r.file(t, "calls"), d) {
		t.Fatal("the key's d member reached gcloud argv")
	}
}

func TestPreviewBudgetMatchesWhatDeployAndSecretsUse(t *testing.T) {
	t.Parallel()
	backends := previewList(t, "BACKENDS")
	secrets := runPreview(t, nil, "secrets", "7")
	deploy := runPreview(t, nil, append([]string{"deploy", "7", "abc123"}, backends...)...)
	calls := deployCalls(t, deploy, "7")
	total := 0
	for _, svc := range backends {
		n, _ := strconv.Atoi(flagValue(calls[svc], "--max-instances"))
		for _, kv := range strings.Split(flagValue(calls[svc], "--set-secrets"), ",") {
			_, ref, _ := strings.Cut(kv, "=")
			name := strings.TrimSuffix(ref, ":latest")
			if !secrets.exists("created-" + name) {
				continue
			}
			if m := regexp.MustCompile(`pool_max_conns=(\d+)$`).FindStringSubmatch(secrets.file(t, "created-"+name)); m != nil {
				p, _ := strconv.Atoi(m[1])
				total += n * p
			}
		}
		if svc == "worker" {
			total++ // the River leader connection
		}
		if svc == "auth" {
			m := regexp.MustCompile(`GOTRUE_DB_MAX_POOL_SIZE: '(\d+)'`).FindStringSubmatch(deploy.file(t, "env-auth-pr-7"))
			p, _ := strconv.Atoi(m[1])
			total += n * p
		}
	}
	if got := budgetFleet(t, runPreview(t, nil, "budget")); got != total {
		t.Fatalf("budget fleet = %d, deploy and secrets sum to %d", got, total)
	}
}
