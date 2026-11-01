// preview_script_test.go runs scripts/ci/preview.sh against a gcloud shim that logs argv and captures stdin.
package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const previewGcloudShim = `echo "$*" >> "$D/calls"
[ ! -e "$D/fail-$1-$2" ] || { echo "ERROR: gcloud $1 $2 failed" >&2; exit 1; }
case "$1 $2 $3" in
"run deploy "*)
  prev=
  for a in "$@"; do
    [ "$prev" = --env-vars-file ] && cp "$a" "$D/env-$3"
    prev=$a
  done ;;
"run jobs deploy")
  prev=
  for a in "$@"; do
    [ "$prev" = --env-vars-file ] && cp "$a" "$D/env-job-$4"
    prev=$a
  done ;;
"secrets create "*) cat > "$D/created-$3" ;;
"secrets list "*)
  f=
  prev=
  for a in "$@"; do
    [ "$prev" = --filter ] && f=$a
    prev=$a
  done
  case "$f" in
  *:\*) cat "$D/list-secrets" 2>/dev/null || : ;;
  labels.preview-pr=*) cat "$D/have-${f#*=}" 2>/dev/null || cat "$D/have" 2>/dev/null || : ;;
  *) cat "$D/have" 2>/dev/null || : ;;
  esac ;;
"run services describe" | "run jobs describe" | "storage buckets describe") [ ! -e "$D/exists" ] || grep -qxF -- "$4" "$D/exists" ;;
"run services update") if grep -qxF -- "$4" "$D/failupdate" 2>/dev/null; then echo "ERROR: update $4 failed" >&2; exit 1; fi ;;
"run services list") cat "$D/list-services" 2>/dev/null || : ;;
"run jobs list") cat "$D/list-jobs" 2>/dev/null || : ;;
"storage buckets list") cat "$D/list-buckets" 2>/dev/null || : ;;
"secrets versions describe")
  prev=
  for a in "$@"; do
    [ "$prev" = --secret ] && name=$a
    prev=$a
  done
  if [ -e "$D/describe-out" ]; then cat "$D/describe-out"; exit 0; fi
  if grep -qxF -- "$name" "$D/missing" 2>/dev/null; then echo "ERROR: NOT_FOUND: $name" >&2; exit 1; fi
  if grep -qxF -- "$name" "$D/denied" 2>/dev/null; then echo "ERROR: PERMISSION_DENIED: no access to $name" >&2; exit 1; fi
  if grep -qxF -- "$name" "$D/disabled" 2>/dev/null; then echo DISABLED; else echo ENABLED; fi ;;
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

// previewCurlShim answers the testIamPermissions call: HTTP 200 and an empty permission set, unless $D/curl-code says otherwise.
func previewCurlShim(prefix string) string {
	return prefix + `out=
prev=
for a in "$@"; do
  [ "$prev" = -o ] && out=$a
  prev=$a
done
[ -z "$out" ] || echo '{}' > "$out"
cat "$D/curl-code" 2>/dev/null || printf 200
`
}

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
	return runPreviewEnv(t, nil, setup, args...)
}

// runPreviewEnv is runPreview with extra KEY=value environment entries.
func runPreviewEnv(t *testing.T, env []string, setup func(dir string), args ...string) previewRun {
	t.Helper()
	dir, shims := t.TempDir(), t.TempDir()
	if setup != nil {
		setup(dir)
	}
	writeShim(t, filepath.Join(shims, "gcloud"), "D='"+dir+"'\n"+previewGcloudShim)
	writeShim(t, filepath.Join(shims, "go"), "shift 2\nexec '"+binPath+"' \"$@\"\n")
	writeShim(t, filepath.Join(shims, "curl"), previewCurlShim("D='"+dir+"'\n"))
	cmd := exec.Command("bash", append([]string{filepath.Join(repoRoot(t), "scripts/ci/preview.sh")}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), append([]string{"RUNNER_TEMP=" + dir}, env...)...), "PATH="+shims+string(os.PathListSeparator)+os.Getenv("PATH"))
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
	for _, name := range []string{"7", "nightly"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := runPreview(t, nil, "secrets", name)
			var keys []map[string]any
			if err := json.Unmarshal([]byte(r.file(t, "created-"+previewSuffix(name)+"-mock-issuer-key")), &keys); err != nil || len(keys) != 1 {
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
		})
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
			total += n // the River leader connection, one per instance
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

func prAll(t *testing.T) []string {
	t.Helper()
	return append(previewList(t, "BACKENDS"), previewList(t, "SITES")...)
}

var (
	goldenMu    sync.Mutex
	goldenCache = map[string]map[string]string{}
	tmpEnvFile  = regexp.MustCompile(`--env-vars-file \S+`)
)

// normCalls sorts the gcloud calls (deploys run four at a time) and masks the temp env-file path.
func normCalls(calls string) string {
	lines := nonBlankLines(tmpEnvFile.ReplaceAllString(calls, "--env-vars-file TMP"))
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}

// nameGoldens runs every PR-name-dependent command for one name and returns file name -> content.
func nameGoldens(t *testing.T, name string) map[string]string {
	t.Helper()
	goldenMu.Lock()
	defer goldenMu.Unlock()
	if g, ok := goldenCache[name]; ok {
		return g
	}
	out := map[string]string{}
	goldenCache[name] = out
	urls := runPreview(t, nil, "urls", name)
	img := runPreview(t, nil, "image", name, "gateway", "abc123")
	out["urls"] = urls.stdout
	out["image"] = img.stdout

	var env []string
	if name == "nightly" {
		env = []string{"NIGHTLY_HUBSPOT_TOKEN_VERSION=7"}
	}
	dep := runPreviewEnv(t, env, nil, append([]string{"deploy", name, "abc123"}, prAll(t)...)...)
	if dep.code != 0 {
		t.Fatalf("deploy exited %d: %s", dep.code, dep.stderr)
	}
	for _, svc := range prAll(t) {
		out["env-"+svc] = dep.file(t, "env-"+svc+"-"+previewSuffix(name))
	}
	out["deploy-argv"] = normCalls(dep.file(t, "calls"))

	mig := runPreview(t, nil, "migrate", name, "abc123")
	out["env-migrate"] = mig.file(t, "env-job-migrate-"+previewSuffix(name))
	out["migrate-calls"] = normCalls(mig.file(t, "calls"))

	sec := runPreview(t, nil, "secrets", name)
	var b strings.Builder
	for _, n := range previewList(t, "PR_SECRETS") {
		f := "created-" + previewSuffix(name) + "-" + n
		b.WriteString(f + "\n")
		if strings.HasPrefix(n, "database-") {
			b.WriteString("  " + sec.file(t, f) + "\n")
		}
	}
	out["secrets-created"] = b.String()
	out["secrets-calls"] = sec.file(t, "calls")
	out["secrets-stdout"] = sec.stdout

	td := runPreview(t, func(dir string) {
		var have strings.Builder
		for _, n := range previewList(t, "PR_SECRETS") {
			have.WriteString(previewSuffix(name) + "-" + n + "\n")
		}
		writeIn(t, dir, "have", have.String())
	}, "teardown", name)
	out["teardown-calls"] = td.file(t, "calls")
	out["teardown-stdout"] = td.stdout
	return out
}

func previewSuffix(name string) string {
	if name == "nightly" {
		return "nightly"
	}
	return "pr-" + name
}

// Set UPDATE_GOLDEN=1 to rewrite the goldens from the current script.
func TestPreviewPRSevenIsByteIdenticalToItsGoldens(t *testing.T) {
	t.Parallel()
	got := nameGoldens(t, "7")
	dir := filepath.Join("testdata", "preview-pr7")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range got {
			writeIn(t, dir, name+".golden", body)
		}
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(got) {
		t.Errorf("%d goldens, %d outputs", len(entries), len(got))
	}
	for name, body := range got {
		want, err := os.ReadFile(filepath.Join(dir, name+".golden"))
		if err != nil {
			t.Fatal(err)
		}
		if string(want) != body {
			t.Errorf("%s differs from its golden:\n--- want\n%s\n--- got\n%s", name, want, body)
		}
		if strings.Contains(strings.ToUpper(body), "OPENROUTER") {
			t.Errorf("PR 7 %s names OpenRouter", name)
		}
	}
	for svc := range nlRealAI {
		if env := got["env-"+svc]; !strings.Contains(env, nlFakes) {
			t.Errorf("PR 7 env-%s lacks the fakes:\n%s", svc, env)
		}
	}
}

func prGolden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "preview-pr7", name+".golden"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// asNightly rewrites a PR 7 output into what the nightly must print.
func asNightly(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "ascomply-preview-pr-7", "ascomply-preview-nightly"), "pr-7", "nightly")
}

func TestPreviewNameArgRefusesNearMisses(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"Nightly", "nightly2", "nightly ", "pr-7", "", "-1", "7a", "0", "07"} {
		for _, cmd := range []string{"urls", "token", "teardown"} {
			t.Run(cmd+"/"+bad, func(t *testing.T) {
				t.Parallel()
				r := runPreview(t, nil, cmd, bad)
				if r.code != 1 || !strings.Contains(r.stderr, "a PR number or nightly is required, got '"+bad+"'") || r.exists("calls") {
					t.Fatalf("exit %d, gcloud called %v, stderr %q", r.code, r.exists("calls"), r.stderr)
				}
			})
		}
	}
}

func TestPreviewNightlyURLsAndImage(t *testing.T) {
	t.Parallel()
	r := runPreview(t, nil, "urls", "nightly")
	const host = "-nightly-944095690109.africa-south1.run.app"
	want := "gateway_url=https://gateway" + host + "\napp_url=https://app" + host + "\nlanding_url=https://landing" + host +
		"\nlibrary_url=https://library" + host + "/library\nblog_url=https://blog" + host + "/blog\nops_console_url=https://ops-console" + host +
		"\nsupport_console_url=https://support-console" + host + "\n"
	if r.stdout != want {
		t.Errorf("urls:\n%s\nwant\n%s", r.stdout, want)
	}
	img := runPreview(t, nil, "image", "nightly", "gateway", "abc123")
	if img.stdout != "africa-south1-docker.pkg.dev/ascomply-preview/images/pr/gateway:nightly-abc123" {
		t.Errorf("image = %q", img.stdout)
	}
}

func TestPreviewNightlyEnvFilesDifferOnlyByName(t *testing.T) {
	t.Parallel()
	got := nameGoldens(t, "nightly")
	for _, svc := range prAll(t) {
		want := asNightly(prGolden(t, "env-"+svc))
		if nlRealAI[svc] {
			if !strings.Contains(want, nlFakes) {
				t.Fatalf("PR golden env-%s lacks the fakes", svc)
			}
			want = strings.Replace(want, nlFakes, "", 1)
		}
		if strings.Replace(got["env-"+svc], nlDealStage, "", 1) != want {
			t.Errorf("env-%s:\n%s\nwant\n%s", svc, got["env-"+svc], want)
		}
	}
	if want := asNightly(prGolden(t, "env-migrate")); got["env-migrate"] != want {
		t.Errorf("migrate env:\n%s\nwant\n%s", got["env-migrate"], want)
	}
}

const nlDealStage = "HUBSPOT_DEAL_STAGE: appointmentscheduled\n"

const (
	nlFakes = "AI_FAKE: 'true'\nJEV_FAKE: 'true'\n"
	nlKey   = "OPENROUTER_API_KEY=nightly-openrouter-api-key:latest"
)

// nlRealAI is the set of nightly services that answer with the real model.
var nlRealAI = map[string]bool{"invoice": true, "submission": true, "worker": true}

var nlContactKeys = []string{"HUBSPOT_TOKEN=nightly-hubspot-token:7", "RESEND_API_KEY=nightly-resend-read-key:latest",
	"RESEND_SEGMENT_ID=nightly-resend-segment-id:latest", "RESEND_TOPIC_ID=nightly-resend-topic-id:latest"}

func TestPreviewNightlyContactsAreReal(t *testing.T) {
	t.Parallel()
	got := nameGoldens(t, "nightly")
	ni := argvBySvc(got["deploy-argv"], "nightly")
	for _, svc := range prAll(t) {
		on := svc == "notifications" || svc == "worker"
		env := got["env-"+svc]
		if strings.Contains(env, "CONTACTS_FAKE") {
			t.Errorf("env-%s has CONTACTS_FAKE", svc)
		}
		if has := strings.Contains(env, nlDealStage); has != on {
			t.Errorf("env-%s has HUBSPOT_DEAL_STAGE = %v, want %v", svc, has, on)
		}
		secrets := flagValue(ni[svc], "--set-secrets")
		for _, kv := range nlContactKeys {
			if has := strings.Contains(secrets, kv); has != on {
				t.Errorf("%s mounts %s = %v, want %v", svc, kv, has, on)
			}
		}
		if !on && (strings.Contains(secrets, "HUBSPOT") || strings.Contains(secrets, "RESEND_")) {
			t.Errorf("%s mounts a contact key: %s", svc, secrets)
		}
	}
}

func TestPreviewPRHasNoContactKeys(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"deploy-argv", "env-notifications", "env-worker"} {
		g := prGolden(t, name)
		for _, w := range []string{"HUBSPOT", "RESEND_", "CONTACTS"} {
			if strings.Contains(g, w) {
				t.Errorf("PR 7 %s names %s", name, w)
			}
		}
	}
	got := nameGoldens(t, "7")
	for _, name := range []string{"deploy-argv", "env-notifications", "env-worker"} {
		if got[name] != prGolden(t, name) {
			t.Errorf("PR 7 %s differs from its golden", name)
		}
	}
}

func TestPreviewNightlyDealStageReachesTheDeployChild(t *testing.T) {
	t.Parallel()
	r := runPreviewEnv(t, []string{"NIGHTLY_HUBSPOT_TOKEN_VERSION=7"}, nil, "deploy", "nightly", "abc123", "notifications", "worker")
	if r.code != 0 {
		t.Fatalf("deploy exited %d: %s", r.code, r.stderr)
	}
	for _, svc := range []string{"notifications", "worker"} {
		if env := r.file(t, "env-"+svc+"-nightly"); !strings.Contains(env, nlDealStage) {
			t.Errorf("env-%s lacks the deal stage:\n%s", svc, env)
		}
	}
}

func TestPreviewNightlyMountsThePinnedToken(t *testing.T) {
	t.Parallel()
	r := runPreviewEnv(t, []string{"NIGHTLY_HUBSPOT_TOKEN_VERSION=7"}, nil, "deploy", "nightly", "abc123", "notifications", "worker")
	if r.code != 0 {
		t.Fatalf("deploy exited %d: %s", r.code, r.stderr)
	}
	calls := argvBySvc(normCalls(r.file(t, "calls")), "nightly")
	for _, svc := range []string{"notifications", "worker"} {
		secrets := flagValue(calls[svc], "--set-secrets")
		for _, kv := range nlContactKeys {
			if !strings.Contains(","+secrets+",", ","+kv+",") {
				t.Errorf("%s --set-secrets lacks %s: %s", svc, kv, secrets)
			}
		}
	}
}

func TestPreviewNightlyRefusesAnUnpinnedToken(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"", "latest", "7;x"} {
		t.Run("version="+v, func(t *testing.T) {
			t.Parallel()
			r := runPreviewEnv(t, []string{"NIGHTLY_HUBSPOT_TOKEN_VERSION=" + v}, nil, "deploy", "nightly", "abc123", "notifications", "worker")
			if r.code == 0 || r.exists("calls") {
				t.Fatalf("exit %d, gcloud called %v", r.code, r.exists("calls"))
			}
		})
	}
	r := runPreviewEnv(t, []string{"NIGHTLY_HUBSPOT_TOKEN_VERSION="}, nil, "deploy", "nightly", "abc123", "app")
	if r.code != 0 || len(nonBlankLines(r.file(t, "calls"))) != 1 {
		t.Fatalf("app deploy exit %d: %s", r.code, r.stderr)
	}
}

func TestPreviewContactCommandsRefuseAPR(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{"contact-preflight", "contact-token", "contact-token-version"} {
		for _, bad := range []string{"7", ""} {
			t.Run(cmd+"/"+bad, func(t *testing.T) {
				t.Parallel()
				r := runPreview(t, nil, cmd, bad)
				if r.code == 0 || r.exists("calls") {
					t.Fatalf("exit %d, gcloud called %v", r.code, r.exists("calls"))
				}
			})
		}
	}
}

func TestPreviewContactTokenVersion(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) { writeIn(t, dir, "describe-out", "7\n") }, "contact-token-version", "nightly")
	if r.code != 0 || r.stdout != "7\n" || !strings.Contains(r.file(t, "calls"), "--secret nightly-hubspot-token ") {
		t.Fatalf("exit %d, stdout %q, calls %q", r.code, r.stdout, r.file(t, "calls"))
	}
	for _, out := range []string{"latest\n", ""} {
		if r := runPreview(t, func(dir string) { writeIn(t, dir, "describe-out", out) }, "contact-token-version", "nightly"); r.code == 0 {
			t.Errorf("output %q: exit 0", out)
		}
	}
}

func TestPreviewContactTokenReadsTheNamedVersion(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"contact-token", "nightly", "7"}, "secrets versions access 7 --secret nightly-hubspot-token "},
		{[]string{"contact-token", "nightly"}, "secrets versions access latest --secret nightly-hubspot-token "},
		{[]string{"contact-token", "nightly", ""}, "secrets versions access latest --secret nightly-hubspot-token "},
	} {
		r := runPreview(t, nil, c.args...)
		calls := nonBlankLines(r.file(t, "calls"))
		if r.code != 0 || len(calls) != 1 || !strings.HasPrefix(calls[0], c.want) {
			t.Errorf("%v: exit %d, calls %v", c.args, r.code, calls)
		}
	}
	r := runPreview(t, nil, "contact-token", "nightly", "7;x")
	if r.code != 1 || r.exists("calls") {
		t.Errorf("7;x: exit %d, gcloud called %v", r.code, r.exists("calls"))
	}
}

var contactSecrets = []string{"nightly-hubspot-token", "nightly-resend-read-key", "nightly-resend-receiving-domain", "nightly-resend-segment-id", "nightly-resend-topic-id"}

func TestPreviewContactPreflightNamesEveryMissingSecret(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) { writeIn(t, dir, "missing", contactSecrets[0]+"\n"+contactSecrets[3]+"\n") }, "contact-preflight", "nightly")
	if r.code != 1 {
		t.Fatalf("exit %d, want 1", r.code)
	}
	for i, n := range contactSecrets {
		if got, want := strings.Contains(r.stderr, n), i == 0 || i == 3; got != want {
			t.Errorf("stderr names %s = %v, want %v:\n%s", n, got, want, r.stderr)
		}
	}
	for i, n := range contactSecrets {
		one := runPreview(t, func(dir string) { writeIn(t, dir, "missing", n+"\n") }, "contact-preflight", "nightly")
		if one.code != 1 || !strings.Contains(one.stderr, n) {
			t.Errorf("secret %d (%s) missing: exit %d, stderr %q", i, n, one.code, one.stderr)
		}
	}
	d := runPreview(t, func(dir string) { writeIn(t, dir, "disabled", contactSecrets[4]+"\n") }, "contact-preflight", "nightly")
	if d.code != 1 || !strings.Contains(d.stderr, contactSecrets[4]) || strings.Contains(d.stderr, contactSecrets[0]) {
		t.Errorf("disabled: exit %d, stderr %q", d.code, d.stderr)
	}
	ok := runPreview(t, nil, "contact-preflight", "nightly")
	if ok.code != 0 {
		t.Errorf("all present: exit %d, stderr %q", ok.code, ok.stderr)
	}
	for _, rr := range []previewRun{r, d, ok} {
		if strings.Contains(rr.file(t, "calls"), "versions access") {
			t.Error("preflight read a secret value")
		}
	}
}

func TestPreviewNightlyIssuer(t *testing.T) {
	t.Parallel()
	got := nameGoldens(t, "nightly")
	if !strings.Contains(got["env-auth"], "GOTRUE_JWT_ISSUER: urn:ascomply:auth:nightly\n") {
		t.Errorf("auth env lacks the nightly issuer:\n%s", got["env-auth"])
	}
	gw := got["env-gateway"]
	for _, w := range []string{`"issuer":"urn:ascomply:auth:nightly"`, `"jwks_url":"https://auth-nightly-944095690109.africa-south1.run.app/.well-known/jwks.json"`, "DEPLOY_ENV: nightly\n"} {
		if !strings.Contains(gw, w) {
			t.Errorf("gateway env lacks %s:\n%s", w, gw)
		}
	}
}

// argvBySvc maps service -> its deploy line from a normalised deploy-argv golden.
func argvBySvc(argv, suffix string) map[string]string {
	out := map[string]string{}
	for _, line := range nonBlankLines(argv) {
		f := strings.Fields(line)
		out[strings.TrimSuffix(f[2], "-"+suffix)] = line
	}
	return out
}

func TestPreviewNightlySetSecretsMirrorThePR(t *testing.T) {
	t.Parallel()
	pr, ni := argvBySvc(prGolden(t, "deploy-argv"), "pr-7"), argvBySvc(nameGoldens(t, "nightly")["deploy-argv"], "nightly")
	for _, svc := range prAll(t) {
		want := strings.ReplaceAll(flagValue(pr[svc], "--set-secrets"), "pr-7-", "nightly-")
		if nlRealAI[svc] {
			want += "," + nlKey
		}
		if svc == "notifications" || svc == "worker" {
			want += "," + strings.Join(nlContactKeys, ",")
		}
		if got := flagValue(ni[svc], "--set-secrets"); got != want {
			t.Errorf("%s --set-secrets = %q, want %q", svc, got, want)
		}
		if !strings.Contains(ni[svc], "--labels preview-pr=nightly ") {
			t.Errorf("%s is not labelled preview-pr=nightly: %s", svc, ni[svc])
		}
	}
}

func TestPreviewNightlyRealAIOnThreeServices(t *testing.T) {
	t.Parallel()
	got := nameGoldens(t, "nightly")
	ni := argvBySvc(got["deploy-argv"], "nightly")
	real := 0
	for _, svc := range prAll(t) {
		env, secrets := got["env-"+svc], flagValue(ni[svc], "--set-secrets")
		if strings.Contains(env, "AI_FAKE") || strings.Contains(env, "JEV_FAKE") {
			t.Errorf("nightly env-%s sets a fake:\n%s", svc, env)
		}
		if strings.Contains(strings.ToUpper(env), "OPENROUTER") {
			t.Errorf("nightly env-%s names the key in plain env:\n%s", svc, env)
		}
		has := strings.Contains(","+secrets+",", ","+nlKey+",")
		if has != nlRealAI[svc] {
			t.Errorf("%s mounts %s = %v, want %v: %s", svc, nlKey, has, nlRealAI[svc], secrets)
		}
		if n := strings.Count(ni[svc], "OPENROUTER"); n != map[bool]int{true: 1, false: 0}[nlRealAI[svc]] {
			t.Errorf("%s argv names OPENROUTER %d times: %s", svc, n, ni[svc])
		}
		if nlRealAI[svc] {
			real++
			if !strings.Contains(env, "DOCUMENT_BUCKET: ascomply-preview-nightly\n") {
				t.Errorf("nightly env-%s lost its document settings:\n%s", svc, env)
			}
		}
	}
	if real != 3 {
		t.Fatalf("%d real-AI services seen, want 3", real)
	}
}

func TestPreviewNightlyDeployMatchesTheBudgetTables(t *testing.T) {
	t.Parallel()
	pr, ni := argvBySvc(prGolden(t, "deploy-argv"), "pr-7"), argvBySvc(nameGoldens(t, "nightly")["deploy-argv"], "nightly")
	for _, svc := range prAll(t) {
		if a, b := flagValue(pr[svc], "--max-instances"), flagValue(ni[svc], "--max-instances"); a != b {
			t.Errorf("%s --max-instances: PR %s, nightly %s", svc, a, b)
		}
	}
	if want := strings.ReplaceAll(prGolden(t, "secrets-created"), "pr-7-", "nightly-"); strings.ReplaceAll(nameGoldens(t, "nightly")["secrets-created"], "/pr_0?", "/pr_7?") != want {
		t.Errorf("nightly secrets and pools differ from the PR's:\n%s", nameGoldens(t, "nightly")["secrets-created"])
	}
	if n := budgetFleet(t, runPreview(t, nil, "budget")); n != 30 {
		t.Errorf("fleet = %d, want 30", n)
	}
}

func TestPreviewSecretsNightlyMakesOnlyThePRSet(t *testing.T) {
	t.Parallel()
	r := runPreview(t, nil, "secrets", "nightly")
	if r.code != 0 {
		t.Fatalf("secrets exited %d: %s", r.code, r.stderr)
	}
	want := map[string]bool{}
	for _, n := range previewList(t, "PR_SECRETS") {
		want["created-nightly-"+n] = true
	}
	entries, _ := os.ReadDir(r.dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "created-") {
			if !want[e.Name()] {
				t.Errorf("unexpected secret %s", e.Name())
			}
			delete(want, e.Name())
		}
	}
	for n := range want {
		t.Errorf("secret %s not created", n)
	}
	calls := r.file(t, "calls")
	if n := strings.Count(calls, "secrets create nightly-"); n != len(previewList(t, "PR_SECRETS")) || strings.Count(calls, "--labels preview-pr=nightly") != n {
		t.Errorf("creates or labels wrong:\n%s", calls)
	}
	for _, db := range []string{"superuser", "migration", "app", "worker", "reader", "gateway", "auth"} {
		if dsn := r.file(t, "created-nightly-database-"+db+"-url"); !strings.Contains(dsn, ":5432/pr_0?sslmode=require") {
			t.Errorf("database-%s-url = %q, want database pr_0", db, dsn)
		}
	}
}

func TestPreviewNightlyBucketDatabaseMigrateGrantToken(t *testing.T) {
	t.Parallel()
	absent := func(dir string) { writeIn(t, dir, "exists", "none\n") }
	b := runPreview(t, absent, "bucket", "nightly")
	bc := b.file(t, "calls")
	for _, w := range []string{"storage buckets create gs://ascomply-preview-nightly ", "storage buckets update gs://ascomply-preview-nightly ", "--update-labels preview-pr=nightly"} {
		if !strings.Contains(bc, w) {
			t.Errorf("bucket calls lack %q:\n%s", w, bc)
		}
	}
	if c := runPreview(t, nil, "database", "nightly").file(t, "calls"); !strings.Contains(c, "run jobs execute preview-db ") || !strings.HasSuffix(strings.TrimSpace(c), "--update-env-vars PR_DB=pr_0,ACTION=recreate") {
		t.Errorf("database calls:\n%s", c)
	}
	m := runPreview(t, nil, "migrate", "nightly", "abc123")
	for _, w := range []string{"run jobs deploy migrate-nightly ", "--labels preview-pr=nightly ", "images/pr/migrate:nightly-abc123", "run jobs execute migrate-nightly "} {
		if !strings.Contains(m.file(t, "calls"), w) {
			t.Errorf("migrate calls lack %q", w)
		}
	}
	env := m.file(t, "env-job-migrate-nightly")
	for _, w := range []string{"DEPLOY_ENV: nightly\n", "DOCUMENT_BUCKET: ascomply-preview-nightly\n"} {
		if !strings.Contains(env, w) {
			t.Errorf("migrate env lacks %q:\n%s", w, env)
		}
	}
	if c := runPreview(t, nil, "grant", "nightly").file(t, "calls"); !strings.HasPrefix(c, "run jobs execute migrate-nightly ") {
		t.Errorf("grant calls:\n%s", c)
	}
	if c := runPreview(t, nil, "token", "nightly").file(t, "calls"); !strings.Contains(c, "--secret nightly-gateway-token ") {
		t.Errorf("token calls:\n%s", c)
	}
}

func TestPreviewTeardownNightlyDeletesOnlyItsOwn(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) {
		writeIn(t, dir, "exists", "gateway-nightly\ngateway-pr-7\nmigrate-nightly\nmigrate-pr-7\ngs://ascomply-preview-nightly\n")
		writeIn(t, dir, "have-nightly", "nightly-gateway-token\nnightly-s2s-token\n") // labelled secrets only; nightly-hubspot-token has no label
		writeIn(t, dir, "have-7", "pr-7-gateway-token\n")
	}, "teardown", "nightly")
	if r.code != 0 || r.stdout != "PR nightly: preview deleted\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
	var deletes []string
	for _, l := range nonBlankLines(r.file(t, "calls")) {
		if strings.Contains(l, " delete ") || strings.Contains(l, "storage rm ") {
			head, _, _ := strings.Cut(l, " --project")
			deletes = append(deletes, head)
		}
	}
	want := []string{"run services delete gateway-nightly", "run jobs delete migrate-nightly", "storage rm --recursive gs://ascomply-preview-nightly",
		"secrets delete nightly-gateway-token", "secrets delete nightly-s2s-token"}
	if got := strings.Join(deletes, "|"); got != strings.Join(want, "|") {
		t.Errorf("deletes = %v, want %v", deletes, want)
	}
	c := r.file(t, "calls")
	for _, w := range []string{"gs://ascomply-preview-nightly", "--filter labels.preview-pr=nightly ", "PR_DB=pr_0,ACTION=drop"} {
		if !strings.Contains(c, w) {
			t.Errorf("calls lack %q", w)
		}
	}
	if strings.Contains(c, "secrets delete nightly-hubspot-token") {
		t.Error("teardown deleted a secret without the preview-pr label")
	}
	if strings.Contains(c, "delete gateway-pr-7") || strings.Contains(c, "migrate-pr-7") || strings.Contains(c, "pr_7") {
		t.Errorf("teardown reached a PR's resources:\n%s", c)
	}
}

func TestPreviewPrsListsTheNightly(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) {
		writeIn(t, dir, "list-services", "nightly 2026-10-29T03:00:00Z\n7 2026-10-28T05:00:00Z\n")
		writeIn(t, dir, "list-jobs", "nightly 2026-10-29T01:00:00Z\n")
		writeIn(t, dir, "list-buckets", "7 2026-10-28T02:00:00Z\n")
		writeIn(t, dir, "list-secrets", "nightly 2026-10-29T00:30:00Z\n7 2026-10-28T06:00:00Z\n")
	}, "prs")
	if want := "nightly 2026-10-29T00:30:00Z\n7 2026-10-28T02:00:00Z\n"; r.code != 0 || r.stdout != want {
		t.Fatalf("exit %d, prs = %q, want %q", r.code, r.stdout, want)
	}
}

func TestPreviewCapCountsEveryPreview(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		entries string
		n       int
		full    bool
	}{
		{"", 0, false},
		{"5 t\n6 t\n", 2, false},
		{"nightly t\n5 t\n6 t\n", 3, true},
		{"nightly t\n5 t\n6 t\n9 t\n", 4, true},
	} {
		t.Run(strconv.Itoa(c.n), func(t *testing.T) {
			t.Parallel()
			r := runPreview(t, func(dir string) { writeIn(t, dir, "list-secrets", c.entries) }, "cap")
			want := "open=" + strconv.Itoa(c.n) + "\nfull=" + strconv.FormatBool(c.full) + "\n"
			if r.code != 0 || r.stdout != want {
				t.Fatalf("exit %d, cap = %q, want %q", r.code, r.stdout, want)
			}
		})
	}
}

var mailSecrets = []string{"nightly-gotrue-smtp-pass", "nightly-resend-sending-key", "nightly-resend-read-key", "nightly-resend-receiving-domain"}

func TestPreviewMailOnMountsOnlyTheNightlysMail(t *testing.T) {
	t.Parallel()
	r := runPreview(t, nil, "mail-on", "nightly")
	if r.code != 0 {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	lines := nonBlankLines(r.file(t, "calls"))
	if len(lines) != 2 {
		t.Fatalf("calls = %v, want two service updates", lines)
	}
	gw := "https://gateway-nightly-944095690109.africa-south1.run.app"
	for _, w := range []string{"run services update auth-nightly ", "--update-secrets GOTRUE_SMTP_PASS=nightly-gotrue-smtp-pass:latest",
		"GOTRUE_MAILER_TEMPLATES_RECOVERY=" + gw + "/emails/recovery.html", "GOTRUE_MAILER_URLPATHS_RECOVERY=" + gw + "/auth/reset-password",
		"GOTRUE_MAILER_SUBJECTS_RECOVERY=Reset your ASComply password", "GOTRUE_MAILER_TEMPLATES_CONFIRMATION=" + gw + "/emails/confirmation.html",
		"GOTRUE_MAILER_URLPATHS_CONFIRMATION=" + gw + "/auth/verify", "GOTRUE_MAILER_SUBJECTS_CONFIRMATION=Confirm your ASComply account",
		"GOTRUE_MAILER_AUTOCONFIRM=false"} {
		if !strings.Contains(lines[0], w) {
			t.Errorf("auth update lacks %q:\n%s", w, lines[0])
		}
	}
	if !strings.HasPrefix(lines[1], "run services update tenancy-nightly ") || !strings.Contains(lines[1], "--update-secrets RESEND_SENDING_KEY=nightly-resend-sending-key:latest") {
		t.Errorf("tenancy update = %q", lines[1])
	}
	if strings.Contains(r.file(t, "calls"), "versions access") {
		t.Error("mail-on read a secret value")
	}
}

func TestPreviewMailCommandsRefuseAPR(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{"mail-preflight", "mail-on", "mail-off", "mail-read-key", "mail-domain"} {
		for _, bad := range []string{"7", ""} {
			t.Run(cmd+"/"+bad, func(t *testing.T) {
				t.Parallel()
				r := runPreview(t, nil, cmd, bad)
				if r.code == 0 || r.exists("calls") {
					t.Fatalf("exit %d, gcloud called %v", r.code, r.exists("calls"))
				}
			})
		}
	}
}

func TestPreviewMailReadCommandsReadOneSecret(t *testing.T) {
	t.Parallel()
	const shimOutput = "shim-output"
	for cmd, name := range map[string]string{"mail-read-key": "nightly-resend-read-key", "mail-domain": "nightly-resend-receiving-domain"} {
		t.Run(cmd, func(t *testing.T) {
			t.Parallel()
			r := runPreview(t, func(dir string) { writeIn(t, dir, "access-"+name, shimOutput) }, cmd, "nightly")
			calls := nonBlankLines(r.file(t, "calls"))
			wantCall := "secrets versions access latest --secret " + name + " "
			if r.code != 0 || r.stdout != shimOutput || len(calls) != 1 || !strings.HasPrefix(calls[0], wantCall) {
				t.Fatalf("exit %d, stdout %q, calls %v", r.code, r.stdout, calls)
			}
		})
	}
}

func TestPreviewMailPreflightNamesEveryMissingSecret(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) { writeIn(t, dir, "missing", mailSecrets[1]+"\n"+mailSecrets[3]+"\n") }, "mail-preflight", "nightly")
	if r.code != 1 {
		t.Fatalf("exit %d, want 1", r.code)
	}
	for i, n := range mailSecrets {
		if got, want := strings.Contains(r.stderr, n), i == 1 || i == 3; got != want {
			t.Errorf("stderr names %s = %v, want %v:\n%s", n, got, want, r.stderr)
		}
	}
	for _, l := range nonBlankLines(r.file(t, "calls")) {
		if !strings.HasPrefix(l, "secrets versions describe ") {
			t.Errorf("unexpected call %q", l)
		}
	}
}

func TestPreviewMailPreflightNamesADisabledSecret(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) { writeIn(t, dir, "disabled", mailSecrets[2]+"\n") }, "mail-preflight", "nightly")
	if r.code != 1 || !strings.Contains(r.stderr, mailSecrets[2]) || strings.Contains(r.stderr, mailSecrets[0]) {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	if !strings.Contains(r.file(t, "calls"), "--format value(state)") {
		t.Errorf("preflight did not read the state: %s", r.file(t, "calls"))
	}
}

func TestPreviewMailPreflightReportsAFailureThatIsNotNotFound(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) { writeIn(t, dir, "denied", mailSecrets[1]+"\n") }, "mail-preflight", "nightly")
	if r.code != 1 || !strings.Contains(r.stderr, "PERMISSION_DENIED") || strings.Contains(r.stderr, "missing or not enabled") {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
}

func TestPreviewMailOffUnmountsAndTurnsAutoConfirmOn(t *testing.T) {
	t.Parallel()
	r := runPreview(t, nil, "mail-off", "nightly")
	if r.code != 0 {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	lines := nonBlankLines(r.file(t, "calls"))
	if len(lines) != 2 {
		t.Fatalf("calls = %v, want two service updates", lines)
	}
	if !strings.HasPrefix(lines[0], "run services update auth-nightly ") || !strings.Contains(lines[0], "--remove-secrets GOTRUE_SMTP_PASS") || !strings.Contains(lines[0], "GOTRUE_MAILER_AUTOCONFIRM=true") {
		t.Errorf("auth update = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "run services update tenancy-nightly ") || !strings.Contains(lines[1], "--remove-secrets RESEND_SENDING_KEY") {
		t.Errorf("tenancy update = %q", lines[1])
	}
}

func TestPreviewMailOffTriesBothServicesAndFailsWhenOneFails(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) { writeIn(t, dir, "failupdate", "auth-nightly\n") }, "mail-off", "nightly")
	if r.code == 0 || !strings.Contains(r.stderr, "mail-off left real mail mounted") {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	if !strings.Contains(r.file(t, "calls"), "run services update tenancy-nightly ") {
		t.Errorf("tenancy was not tried after auth failed: %s", r.file(t, "calls"))
	}
}

func TestPreviewMailPreflightPassesWithAllFour(t *testing.T) {
	t.Parallel()
	r := runPreview(t, nil, "mail-preflight", "nightly")
	calls := nonBlankLines(r.file(t, "calls"))
	if r.code != 0 || len(calls) != 4 {
		t.Fatalf("exit %d, calls %v", r.code, calls)
	}
	for i, n := range mailSecrets {
		if !strings.Contains(calls[i], "--secret "+n+" ") {
			t.Errorf("call %d = %q, want secret %s", i, calls[i], n)
		}
	}
}

// invVar is one env entry of a Cloud Run service: a plain value, or a reference to a secret.
type invVar struct{ name, value, ref string }

type invSvc struct {
	name string
	vars []invVar
}

const nlKeySecret = "nightly-openrouter-api-key"

func nlKeyVar() invVar { return invVar{name: "OPENROUTER_API_KEY", ref: nlKeySecret} }

// nlGoodSvcs are the three services that may hold the nightly key.
func nlGoodSvcs() []invSvc {
	return []invSvc{{"invoice-nightly", []invVar{nlKeyVar()}}, {"submission-nightly", []invVar{nlKeyVar()}}, {"worker-nightly", []invVar{nlKeyVar()}}}
}

// runInvariants runs `preview.sh invariants` over the listed secret names and services.
func runInvariants(t *testing.T, secrets []string, svcs []invSvc) previewRun {
	t.Helper()
	var list []map[string]any
	for _, s := range svcs {
		env := []map[string]any{}
		for _, v := range s.vars {
			e := map[string]any{"name": v.name}
			if v.ref != "" {
				e["valueFrom"] = map[string]any{"secretKeyRef": map[string]any{"name": v.ref, "key": "latest"}}
			} else {
				e["value"] = v.value
			}
			env = append(env, e)
		}
		list = append(list, map[string]any{"metadata": map[string]any{"name": s.name},
			"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"env": env}}}}}})
	}
	b, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	return runPreview(t, func(dir string) {
		writeIn(t, dir, "have", strings.Join(secrets, "\n")+"\n")
		writeIn(t, dir, "list-services", string(b))
	}, "invariants")
}

// invLine reports whether any output line starts with the service name and a space.
func invLine(r previewRun, svc string) bool {
	return regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(svc) + ` `).MatchString(r.stdout + "\n" + r.stderr)
}

func TestPreviewInvariantsAllowTheNightlyKey(t *testing.T) {
	t.Parallel()
	r := runInvariants(t, []string{nlKeySecret, "pr-7-gateway-token"}, nlGoodSvcs())
	if r.code != 0 {
		t.Fatalf("invariants exited %d:\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stdout, "Preview invariants hold") {
		t.Errorf("no success line:\n%s", r.stdout)
	}
}

func TestPreviewInvariantsPassWithNoKeyAtAll(t *testing.T) {
	t.Parallel()
	r := runInvariants(t, []string{"pr-7-gateway-token"}, []invSvc{{"invoice-pr-7", []invVar{{name: "AI_FAKE", value: "true"}}}, {"invoice-nightly", nil}})
	if r.code != 0 || !strings.Contains(r.stdout, "Preview invariants hold") {
		t.Fatalf("invariants exited %d:\n%s\n%s", r.code, r.stdout, r.stderr)
	}
}

func TestPreviewInvariantsRefuseAnotherOpenRouterSecret(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"openrouter-api-key", "nightly-openrouter-api-key-2", "pr-7-openrouter", "NIGHTLY-OPENROUTER-API-KEY", "xnightly-openrouter-api-key"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := runInvariants(t, []string{nlKeySecret, name}, nlGoodSvcs())
			if r.code != 1 {
				t.Fatalf("invariants exited %d, want 1:\n%s\n%s", r.code, r.stdout, r.stderr)
			}
			if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `$`).MatchString(r.stdout + "\n" + r.stderr) {
				t.Errorf("output does not list %s:\n%s\n%s", name, r.stdout, r.stderr)
			}
		})
	}
}

func TestPreviewInvariantsRefuseTheKeyOnAPRService(t *testing.T) {
	t.Parallel()
	r := runInvariants(t, []string{nlKeySecret}, append(nlGoodSvcs(), invSvc{"invoice-pr-7", []invVar{nlKeyVar()}}))
	if r.code != 1 {
		t.Fatalf("invariants exited %d, want 1:\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	if !invLine(r, "invoice-pr-7") {
		t.Errorf("no line names invoice-pr-7:\n%s\n%s", r.stdout, r.stderr)
	}
	for _, ok := range []string{"invoice-nightly", "submission-nightly", "worker-nightly"} {
		if invLine(r, ok) {
			t.Errorf("a line names the allowed %s:\n%s\n%s", ok, r.stdout, r.stderr)
		}
	}
}

func TestPreviewInvariantsRefuseTheKeyOnAnotherNightlyService(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"gateway-nightly", "nightly", "invoice-nightly-2", "xinvoice-nightly", "invoice-nightlyx", "tenancy-nightly"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := runInvariants(t, []string{nlKeySecret}, append(nlGoodSvcs(), invSvc{name, []invVar{nlKeyVar()}}))
			if r.code != 1 {
				t.Fatalf("invariants exited %d, want 1:\n%s\n%s", r.code, r.stdout, r.stderr)
			}
			if !invLine(r, name) {
				t.Errorf("no line names %s:\n%s\n%s", name, r.stdout, r.stderr)
			}
			for _, ok := range []string{"invoice-nightly", "submission-nightly", "worker-nightly"} {
				if ok != name && invLine(r, ok) {
					t.Errorf("a line names the allowed %s:\n%s\n%s", ok, r.stdout, r.stderr)
				}
			}
		})
	}
}

func TestPreviewInvariantsRefuseAPlainKeyOrAnotherSecret(t *testing.T) {
	t.Parallel()
	const plain = "sk-or-v1-notarealkey"
	other := invVar{name: "OPENROUTER_API_KEY", ref: "other"}
	plainKey := invVar{name: "OPENROUTER_API_KEY", value: plain}
	for name, vars := range map[string][]invVar{
		"plain value":                {plainKey},
		"another secret":             {other},
		"a pr secret":                {{name: "OPENROUTER_API_KEY", ref: "pr-7-openrouter"}},
		"the key renamed":            {{name: "OPENROUTER_API_KEY", ref: "nightly-openrouter-api-key-2"}},
		"plain value beside the ref": {nlKeyVar(), plainKey},
		"another secret beside it":   {nlKeyVar(), other},
		"plain value before the ref": {plainKey, nlKeyVar()},
		"another secret before it":   {other, nlKeyVar()},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := runInvariants(t, []string{nlKeySecret}, []invSvc{{"submission-nightly", []invVar{nlKeyVar()}}, {"invoice-nightly", vars}})
			if r.code != 1 {
				t.Fatalf("invariants exited %d, want 1:\n%s\n%s", r.code, r.stdout, r.stderr)
			}
			if !invLine(r, "invoice-nightly") {
				t.Errorf("no line names invoice-nightly:\n%s\n%s", r.stdout, r.stderr)
			}
			if invLine(r, "submission-nightly") {
				t.Errorf("a line names the allowed submission-nightly:\n%s\n%s", r.stdout, r.stderr)
			}
			if strings.Contains(r.stdout+r.stderr, plain) {
				t.Errorf("output leaks the key value:\n%s\n%s", r.stdout, r.stderr)
			}
		})
	}
}

func TestPreviewInvariantsRefuseTheKeySecretUnderAnotherVariableName(t *testing.T) {
	t.Parallel()
	for name, svc := range map[string]invSvc{
		"a pr service":     {"invoice-pr-7", []invVar{{name: "OPENROUTER_KEY", ref: nlKeySecret}}},
		"another nightly":  {"gateway-nightly", []invVar{{name: "AI_TOKEN", ref: nlKeySecret}}},
		"beside the mount": {"invoice-nightly", []invVar{nlKeyVar(), {name: "SECOND_COPY", ref: nlKeySecret}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := runInvariants(t, []string{nlKeySecret}, []invSvc{{"submission-nightly", []invVar{nlKeyVar()}}, svc})
			if r.code != 1 {
				t.Fatalf("invariants exited %d, want 1:\n%s\n%s", r.code, r.stdout, r.stderr)
			}
			if !invLine(r, svc.name) {
				t.Errorf("no line names %s:\n%s\n%s", svc.name, r.stdout, r.stderr)
			}
		})
	}
}

// The volume shape is the Cloud Run v1 (Knative) Volume resource that
// `gcloud run services describe --format=json` prints: spec.template.spec.volumes[].secret.secretName.
func TestPreviewInvariantsRefuseTheKeySecretAsAVolume(t *testing.T) {
	t.Parallel()
	for _, svc := range []string{"invoice-pr-7", "gateway-nightly", "invoice-nightly"} {
		t.Run(svc, func(t *testing.T) {
			t.Parallel()
			vol := map[string]any{"name": "key", "secret": map[string]any{"secretName": nlKeySecret, "items": []any{map[string]any{"key": "latest", "path": "k"}}}}
			list := []map[string]any{}
			for _, s := range append(nlGoodSvcs(), invSvc{name: "gateway-nightly"}, invSvc{name: "invoice-pr-7"}) {
				spec := map[string]any{"containers": []any{map[string]any{"env": []any{}}}}
				if s.name == svc {
					spec["volumes"] = []any{vol}
				}
				list = append(list, map[string]any{"metadata": map[string]any{"name": s.name}, "spec": map[string]any{"template": map[string]any{"spec": spec}}})
			}
			b, err := json.Marshal(list)
			if err != nil {
				t.Fatal(err)
			}
			r := runPreview(t, func(dir string) {
				writeIn(t, dir, "have", nlKeySecret+"\n")
				writeIn(t, dir, "list-services", string(b))
			}, "invariants")
			if r.code != 1 {
				t.Fatalf("invariants exited %d, want 1:\n%s\n%s", r.code, r.stdout, r.stderr)
			}
			if !invLine(r, svc) {
				t.Errorf("no line names %s:\n%s\n%s", svc, r.stdout, r.stderr)
			}
		})
	}
}

func TestPreviewInvariantsFailWhenAListingFails(t *testing.T) {
	t.Parallel()
	for _, fail := range []string{"fail-secrets-list", "fail-run-services"} {
		t.Run(fail, func(t *testing.T) {
			t.Parallel()
			r := runPreview(t, func(dir string) {
				writeIn(t, dir, "have", "pr-7-gateway-token\n")
				writeIn(t, dir, "list-services", "[]")
				writeIn(t, dir, fail, "")
			}, "invariants")
			if r.code == 0 || strings.Contains(r.stdout, "Preview invariants hold") {
				t.Fatalf("a failed listing passed, exit %d:\n%s\n%s", r.code, r.stdout, r.stderr)
			}
			if !strings.Contains(r.stderr, "failed") {
				t.Errorf("the failure is not the listing's:\n%s", r.stderr)
			}
		})
	}
}

func TestPreviewInvariantsRefuseSentryOnTheNightly(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"nightly-sentry-dsn", "pr-7-sentry", "SENTRY-DSN"} {
		t.Run("secret "+name, func(t *testing.T) {
			t.Parallel()
			r := runInvariants(t, []string{nlKeySecret, name}, nlGoodSvcs())
			if r.code != 1 {
				t.Fatalf("invariants exited %d, want 1:\n%s\n%s", r.code, r.stdout, r.stderr)
			}
			if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `$`).MatchString(r.stdout + "\n" + r.stderr) {
				t.Errorf("output does not list %s:\n%s\n%s", name, r.stdout, r.stderr)
			}
		})
	}
	for _, svc := range []string{"gateway-nightly", "invoice-nightly", "worker-nightly", "gateway-pr-7"} {
		for _, name := range []string{"SENTRY_DSN", "VITE_SENTRY_DSN", "SENTRY_AUTH_TOKEN"} {
			for _, v := range []invVar{{name: name, value: "https://k@o.ingest.sentry.io/1"}, {name: name, ref: "nightly-sentry-dsn"}} {
				t.Run(svc+" "+name, func(t *testing.T) {
					t.Parallel()
					vars := []invVar{v}
					if nlRealAI[strings.TrimSuffix(svc, "-nightly")] {
						vars = append(vars, nlKeyVar())
					}
					r := runInvariants(t, []string{nlKeySecret}, []invSvc{{"submission-nightly", []invVar{nlKeyVar()}}, {svc, vars}})
					if r.code != 1 {
						t.Fatalf("invariants exited %d, want 1:\n%s\n%s", r.code, r.stdout, r.stderr)
					}
					if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(svc) + ` ` + name + `( |$)`).MatchString(r.stdout + "\n" + r.stderr) {
						t.Errorf("no line names %s %s:\n%s\n%s", svc, name, r.stdout, r.stderr)
					}
				})
			}
		}
	}
}
