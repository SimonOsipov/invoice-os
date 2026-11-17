// preview_script_test.go runs scripts/ci/preview.sh against a gcloud shim that logs argv and captures stdin.
package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const previewGcloudShim = `echo "$*" >> "$D/calls"
echo gcloud >> "$D/order"
[ ! -e "$D/fail-$1-$2-$3" ] || { echo "ERROR: gcloud $1 $2 $3 failed" >&2; exit 1; }
[ ! -e "$D/fail-$1-$2" ] || { echo "ERROR: gcloud $1 $2 failed" >&2; exit 1; }
[ ! -e "$D/failonce-$1-$2" ] || { rm "$D/failonce-$1-$2"; echo PARTIAL; echo "ERROR: gcloud $1 $2 failed once" >&2; exit 1; }
runfound() {
  if grep -qxF -- "$4" "$D/describe-run-error" 2>/dev/null; then echo "ERROR: (gcloud.run.describe) UNAVAILABLE: backend error" >&2; return 1; fi
  if [ -e "$D/exists" ] && ! grep -qxF -- "$4" "$D/exists"; then echo "ERROR: (gcloud.run.describe) NOT_FOUND: $4 not found." >&2; return 1; fi
}
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
"secrets create "*)
  if grep -qxF -- "$3" "$D/taken" 2>/dev/null; then echo "ERROR: (gcloud.secrets.create) ALREADY_EXISTS: Secret [projects/1/secrets/$3] already exists." >&2; exit 1; fi
  cat > "$D/created-$3" ;;
"secrets describe "*) if grep -qxF -- "$3" "$D/describe-error" 2>/dev/null; then echo "ERROR: (gcloud.secrets.describe) UNAVAILABLE: backend error" >&2; exit 1; fi
  cat "$D/owner-$3" 2>/dev/null || { echo "ERROR: (gcloud.secrets.describe) NOT_FOUND: Secret [projects/1/secrets/$3] not found." >&2; exit 1; } ;;
"storage ls "*)
  if [ -e "$D/no-objects" ]; then echo "ERROR: (gcloud.storage.ls) One or more URLs matched no objects." >&2; exit 1; fi
  echo "$3" ;;
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
"run services describe") case "$*" in *latestReadyRevisionName*)
  [ ! -e "$D/faildescribe" ] || { echo "ERROR: describe $4 failed" >&2; exit 1; }
  cat "$D/latest-$4"; exit 0 ;; esac
  runfound "$@" ;;
"run revisions list")
  prev=
  for a in "$@"; do
    [ "$prev" = --service ] && cat "$D/revisions-$a" 2>/dev/null
    prev=$a
  done ;;
"run revisions delete") if grep -qxF -- "$4" "$D/faildelete" 2>/dev/null; then echo "ERROR: delete $4 failed" >&2; exit 1; fi
  echo "$4" >> "$D/deleted" ;;
"run jobs describe") runfound "$@" ;;
"storage buckets describe") [ ! -e "$D/exists" ] || grep -qxF -- "$4" "$D/exists" ;;
"run services update") if grep -qxF -- "$4" "$D/failupdate" 2>/dev/null; then echo "ERROR: update $4 failed" >&2; exit 1; fi ;;
"services list "*) cat "$D/enabled-services" 2>/dev/null || : ;;
"run services list") cat "$D/list-services" 2>/dev/null || : ;;
"run jobs list") cat "$D/list-jobs" 2>/dev/null || : ;;
"storage buckets list") cat "$D/list-buckets" 2>/dev/null || : ;;
"sql databases list") cat "$D/list-databases" 2>/dev/null || : ;;
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
  if grep -qxF -- "$name" "$D/denied" 2>/dev/null; then echo "ERROR: (gcloud.secrets.versions.access) PERMISSION_DENIED: Permission 'secretmanager.versions.access' denied for resource 'projects/1/secrets/$name/versions/latest' (or it may not exist)." >&2; exit 1; fi
  if grep -qxF -- "$name" "$D/missing" 2>/dev/null; then echo "ERROR: (gcloud.secrets.versions.access) NOT_FOUND: Secret [projects/1/secrets/$name] not found or has no versions." >&2; exit 1; fi
  if grep -qxF -- "$name" "$D/otherdenied" 2>/dev/null; then echo "ERROR: (gcloud.secrets.versions.access) PERMISSION_DENIED: Secret Manager API has not been used in project 1 or it is disabled." >&2; exit 1; fi
  if grep -qxF -- "$name" "$D/accesserr" 2>/dev/null; then echo "ERROR: INTERNAL: backend error on $name" >&2; exit 1; fi
  cat "$D/access-$name" 2>/dev/null || printf pw ;;
"sql instances describe") echo 10.0.0.1 ;;
"run jobs execute")
  n=$(cat "$D/execfails" 2>/dev/null || echo 0)
  if [ "$n" -gt 0 ]; then
    echo $((n - 1)) >"$D/execfails"
    printf 'ERROR: (gcloud.run.jobs.execute) The execution failed.\nView details about this execution by running:\ngcloud run jobs executions describe %s-x%s\n' "$4" "$n" >&2
    exit 1
  fi ;;
"logging read "*) cat "$D/logs" 2>/dev/null || : ;;
esac
`

// previewCurlShim answers the testIamPermissions call: HTTP 200 and an empty permission set, unless $D/curl-code says otherwise.
// It logs argv to $D/curl-calls and stdin to $D/curl-stdin; $D/curl-body-<last url part> and $D/curl-code-<same> answer one endpoint.
func previewCurlShim(prefix string) string {
	return prefix + `echo "$*" >> "$D/curl-calls"
case " $* " in *" -H @- "*) cat >> "$D/curl-stdin" ;; esac
out=
prev=
for a in "$@"; do
  [ "$prev" = -o ] && out=$a
  prev=$a
  ep=${a##*/}
done
if [ -n "$out" ]; then
  cp "$D/curl-body-$ep" "$out" 2>/dev/null || echo '{}' > "$out"
fi
if [ -s "$D/curl-exit-$ep" ]; then exit "$(cat "$D/curl-exit-$ep")"; fi
if [ -s "$D/curl-seq-$ep" ]; then
  head -n 1 "$D/curl-seq-$ep" | tr -d '\n'
  sed -i.bak 1d "$D/curl-seq-$ep"
  exit 0
fi
cat "$D/curl-code-$ep" 2>/dev/null || cat "$D/curl-code" 2>/dev/null || printf 200
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
	writeIn(t, dir, "owner-slot-1-claim", "7") // PR 7 holds slot-1 unless a test removes it
	if setup != nil {
		setup(dir)
	}
	writeShim(t, filepath.Join(shims, "gcloud"), "D='"+dir+"'\n"+previewGcloudShim)
	writeShim(t, filepath.Join(shims, "go"), "shift 2\nexec '"+binPath+"' \"$@\"\n")
	writeShim(t, filepath.Join(shims, "curl"), previewCurlShim("D='"+dir+"'\n"))
	writeShim(t, filepath.Join(shims, "sleep"), "echo \"sleep $*\" >> '"+dir+"/order'\n") // a retry wait costs no time, and is logged
	cmd := exec.Command("bash", append([]string{filepath.Join(repoRoot(t), "scripts/ci/preview.sh")}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(append(slices.DeleteFunc(os.Environ(), func(e string) bool { return strings.HasPrefix(e, "NIGHTLY_KEYS_CLOSED=") }), append([]string{"RUNNER_TEMP=" + dir}, env...)...), "PATH="+shims+string(os.PathListSeparator)+os.Getenv("PATH"))
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
		"app": "3", "landing": "3", "library": "3", "blog": "3", "check": "3", "ops-console": "3", "support-console": "3"}
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

// Workflows and scripts are text no running code reads in a test; only a text read sees a site missing from a deploy list.
func TestPreviewSites_EveryDeployListNamesEachSite(t *testing.T) {
	t.Parallel()
	sites := previewList(t, "SITES")
	if len(sites) < 7 || !slices.Contains(sites, "check") || !slices.Contains(sites, "app") {
		t.Fatalf("SITES = %v, want the seven sites, check and app among them", sites)
	}
	root := repoRoot(t)
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	line := func(name, text, pattern string) []string {
		m := regexp.MustCompile(pattern).FindStringSubmatch(text)
		if m == nil {
			t.Fatalf("%s: no line matches %s", name, pattern)
		}
		return strings.Fields(m[1])
	}
	devEnv, prod := read(".github/workflows/dev-env.yml"), read(".github/workflows/deploy-prod.yml")
	lists := map[string][]string{
		"dev-env deploy sites":   line("dev-env", devEnv, `(?m)^\s+run: bash scripts/ci/preview\.sh deploy "\$PR" "\$SHA" (app [a-z -]+)$`),
		"deploy-prod no-traffic": line("deploy-prod", prod, `(?m)^\s+(app [a-z -]+) \|$`),
		"gcp-traffic SERVICES":   strings.Fields(strings.Join(line("gcp-traffic", read("scripts/ci/gcp-traffic.sh"), `(?s)SERVICES=\(([^)]*)\)`), " ")),
	}
	for name, got := range lists {
		for _, site := range sites {
			if !slices.Contains(got, site) {
				t.Errorf("%s lacks %s: %v", name, site, got)
			}
		}
	}
	if !strings.Contains(prod, "www.ascomply.com/check ") || !strings.Contains(prod, "www.ascomply.com/library ") {
		t.Errorf("deploy-prod's site wait loop lacks a /library or /check address")
	}
	targets := line("dev-env targets", devEnv, `(?m)^\s+for v in (GATEWAY_URL [A-Z_ ]+); do$`)
	wait := line("dev-env wait", devEnv, `(?m)^\s+run: bash scripts/ci/wait-spa-builds\.sh (.*)$`)
	dependabot := read(".github/dependabot.yml")
	for _, site := range sites {
		row := regexp.MustCompile(`\{ image: ` + site + `, dockerfile: frontend/` + site + `/Dockerfile, spa: true \}`)
		for name, text := range map[string]string{"dev-env": devEnv, "deploy-prod": prod} {
			if !row.MatchString(text) {
				t.Errorf("%s build matrix has no row for %s", name, site)
			}
		}
		if v := `"$` + strings.ToUpper(strings.ReplaceAll(site, "-", "_")) + `_URL"`; !slices.Contains(wait, v) {
			t.Errorf("wait-spa-builds call lacks %s: %v", v, wait)
		}
		name := strings.ReplaceAll(site, "-", "_")
		if !strings.Contains(devEnv, "      "+name+"_url: ${{ steps.urls.outputs."+name+"_url }}\n") {
			t.Errorf("dev-env prepare-env publishes no %s_url", name)
		}
		if !slices.Contains(targets, strings.ToUpper(name)+"_URL") {
			t.Errorf("dev-env topology target loop lacks %s_URL: %v", strings.ToUpper(name), targets)
		}
		if n := strings.Count(devEnv, "      "+strings.ToUpper(name)+"_URL: ${{ needs.prepare-env.outputs."+name+"_url }}\n"); n < 2 {
			t.Errorf("dev-env e2e and topology jobs set %s_URL %d times, want 2", strings.ToUpper(name), n)
		}
		if !strings.Contains(dependabot, "\n      - /frontend/"+site+"\n") {
			t.Errorf("dependabot docker directories lack /frontend/%s", site)
		}
	}
}

func TestPreviewGatewayCORSNamesTheCheckOrigin(t *testing.T) {
	t.Parallel()
	r := runPreview(t, nil, "deploy", "7", "abc123", "gateway")
	env := r.file(t, "env-gateway-pr-7")
	const origin = "https://check-pr-7-944095690109.africa-south1.run.app"
	for _, l := range strings.Split(env, "\n") {
		if v, ok := strings.CutPrefix(l, "CORS_ALLOWED_ORIGINS: "); ok {
			if !slices.Contains(strings.Split(v, ","), origin) {
				t.Errorf("CORS_ALLOWED_ORIGINS = %q, want %s in it", v, origin)
			}
			return
		}
	}
	t.Fatalf("gateway env has no CORS_ALLOWED_ORIGINS:\n%s", env)
}

func TestPreviewCheckDeploysAsAPublicSite(t *testing.T) {
	t.Parallel()
	r := runPreview(t, nil, "deploy", "7", "abc123", "check")
	call := deployCalls(t, r, "7")["check"]
	if call == "" {
		t.Fatalf("no check deploy call:\n%s", r.file(t, "calls"))
	}
	if got := flagValue(call, "--memory"); got != "256Mi" {
		t.Errorf("--memory = %q, want 256Mi", got)
	}
	if got := flagValue(call, "--image"); got != "africa-south1-docker.pkg.dev/ascomply-preview/previews/check:pr-7-abc123" {
		t.Errorf("--image = %q", got)
	}
	if !strings.Contains(call, "--no-invoker-iam-check") {
		t.Errorf("no --no-invoker-iam-check: %s", call)
	}
	for _, f := range []string{"--network", "--subnet", "--vpc-egress"} {
		if strings.Contains(call, f) {
			t.Errorf("check call carries %s: %s", f, call)
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
			tok := r.file(t, "created-"+previewSuffix(name)+"-mock-issuer-token")
			if tok == "" || strings.Contains(r.stdout, tok) || strings.Contains(r.stderr, tok) || strings.Contains(r.file(t, "calls"), tok) {
				t.Fatal("the mock issuer token is empty or reached stdout, stderr or gcloud argv")
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

// asNightly rewrites a PR 7 output into what the nightly must print: PR 7 holds slot-1, the nightly the fixed slot "nightly".
func asNightly(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "slot-1", "nightly"), "pr-7", "nightly")
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
		"\nlibrary_url=https://library" + host + "/library\nblog_url=https://blog" + host + "/blog\ncheck_url=https://check" + host + "/check\nops_console_url=https://ops-console" + host +
		"\nsupport_console_url=https://support-console" + host + "\n"
	if r.stdout != want {
		t.Errorf("urls:\n%s\nwant\n%s", r.stdout, want)
	}
	img := runPreview(t, nil, "image", "nightly", "gateway", "abc123")
	if img.stdout != "africa-south1-docker.pkg.dev/ascomply-preview/previews/gateway:nightly-abc123" {
		t.Errorf("image = %q", img.stdout)
	}
}

func TestPreviewImagesUseOnlyThePreviewRepository(t *testing.T) {
	t.Parallel()
	wf, err := os.ReadFile(filepath.Join(repoRoot(t), ".github/workflows/deploy-prod.yml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^\s*REGISTRY:\s*(\S+)`).FindSubmatch(wf)
	if m == nil {
		t.Fatal("no REGISTRY in deploy-prod.yml")
	}
	prodRegistry := string(m[1])
	prodRepo := prodRegistry[strings.LastIndex(prodRegistry, "/"):] + "/"
	const prefix = "africa-south1-docker.pkg.dev/ascomply-preview/previews/"
	for _, name := range []string{"7", "nightly"} {
		var runs []previewRun
		for _, svc := range append(previewList(t, "BACKENDS"), previewList(t, "SITES")...) {
			runs = append(runs, runPreview(t, nil, "deploy", name, "abc123", svc))
		}
		runs = append(runs, runPreview(t, nil, "migrate", name, "abc123"))
		images := 0
		for _, r := range runs {
			if !r.exists("calls") {
				continue
			}
			for _, line := range strings.Split(r.file(t, "calls"), "\n") {
				img := flagValue(line, "--image")
				if img == "" {
					continue
				}
				images++
				if !strings.HasPrefix(img, prefix) {
					t.Errorf("%s: --image %q is outside %s", name, img, prefix)
				}
				if strings.Contains(img, prodRepo) {
					t.Errorf("%s: --image %q names the production repository %s", name, img, prodRepo)
				}
			}
		}
		if images < 15 {
			t.Errorf("%s: saw %d --image flags, want at least 15", name, images)
		}
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
			want = strings.Replace(want, nlFakes, nlAttribution, 1)
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
	// nlAttribution is what the nightly prints where a PR prints nlFakes.
	nlAttribution = "OPENROUTER_APP_NAME: Nightly App\nOPENROUTER_APP_URL: https://app-nightly-944095690109.africa-south1.run.app\n"
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
				if r.code == 0 || r.exists("calls") || r.exists("curl-calls") {
					t.Fatalf("exit %d, gcloud called %v, curl called %v", r.code, r.exists("calls"), r.exists("curl-calls"))
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
		want := strings.ReplaceAll(strings.ReplaceAll(flagValue(pr[svc], "--set-secrets"), "pr-7-", "nightly-"), "slot-1-", "nightly-")
		if nlRealAI[svc] {
			want += "," + nlKey
		}
		if svc == "notifications" || svc == "worker" {
			want += "," + strings.Join(nlContactKeys, ",")
		}
		got := flagValue(ni[svc], "--set-secrets")
		if got != want {
			t.Errorf("%s --set-secrets = %q, want %q", svc, got, want)
		}
		if svc == "gateway" && !strings.Contains(got, "GATEWAY_MOCK_ISSUER_TOKEN=nightly-mock-issuer-token:latest") {
			t.Errorf("nightly gateway lacks its mock issuer token mount: %q", got)
		}
		if has := strings.Contains(got, "mock-issuer-token"); has != (svc == "gateway") {
			t.Errorf("%s mounts the mock issuer token = %v", svc, has)
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
		if strings.Contains(env, "OPENROUTER_API_KEY") {
			t.Errorf("nightly env-%s names the key in plain env:\n%s", svc, env)
		}
		if has := strings.Contains(env, nlAttribution); has != nlRealAI[svc] {
			t.Errorf("nightly env-%s carries the attribution = %v, want %v:\n%s", svc, has, nlRealAI[svc], env)
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
			if !strings.Contains(env, "DOCUMENT_BUCKET: ascomply-preview-docs-nightly\n") {
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

// No allowed mix of PR previews and the nightly may exceed the 95 usable Cloud SQL slots. The counts come from `cap`.
func TestPreviewCapAllowsNoMixOverTheBudget(t *testing.T) {
	t.Parallel()
	capFull := func(held int, caller string) bool {
		var list strings.Builder
		for i := 1; i <= held; i++ {
			list.WriteString(strconv.Itoa(i) + " t\n")
		}
		r := runPreview(t, func(dir string) { writeIn(t, dir, "list-secrets", list.String()) }, "cap", caller)
		return strings.Contains(r.stdout, "full=true")
	}
	maxPR := 0
	for capFull(maxPR, "99") == false {
		maxPR++
		if maxPR > 10 {
			t.Fatal("a new PR is never refused")
		}
	}
	// The nightly may deploy beside every allowed PR preview, and is refused one PR preview later.
	if capFull(maxPR, "nightly") || !capFull(maxPR+1, "nightly") {
		t.Fatalf("nightly cap does not sit one slot above the PR cap of %d", maxPR)
	}
	fleet := budgetFleet(t, runPreview(t, nil, "budget"))
	if got := (maxPR + 1) * fleet; got > 95 {
		t.Errorf("(%d PR previews + the nightly) x %d connections = %d, over 95", maxPR, fleet, got)
	}
}

func TestPreviewSecretsNightlyMakesThePRSet(t *testing.T) {
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
	if n := strings.Count(calls, "secrets create nightly-"); n != len(previewList(t, "PR_SECRETS")) || strings.Count(calls, "--labels preview-pr=nightly ") != n {
		t.Errorf("creates or labels wrong:\n%s", calls)
	}
	if tok := r.file(t, "created-nightly-mock-issuer-token"); !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(tok) {
		t.Errorf("mock issuer token = %q, want 64 lowercase hex and no newline", tok)
	}
	if !strings.Contains(r.stdout, "created nightly-mock-issuer-token\n") {
		t.Errorf("stdout lacks the created line:\n%s", r.stdout)
	}
	for _, db := range []string{"superuser", "migration", "app", "worker", "reader", "gateway", "auth"} {
		if dsn := r.file(t, "created-nightly-database-"+db+"-url"); !strings.Contains(dsn, ":5432/pr_0?sslmode=require") {
			t.Errorf("database-%s-url = %q, want database pr_0", db, dsn)
		}
	}
}

func TestPreviewNightlyBucketDatabaseMigrateGrantToken(t *testing.T) {
	t.Parallel()
	// The nightly's bucket is the Terraform slot: TestPreviewBucketNightlyUsesItsFixedSlot.
	if c := runPreview(t, nil, "database", "nightly").file(t, "calls"); !strings.Contains(c, "run jobs execute preview-db ") || !strings.HasSuffix(strings.TrimSpace(c), "--update-env-vars PR_DB=pr_0,ACTION=recreate") {
		t.Errorf("database calls:\n%s", c)
	}
	m := runPreview(t, nil, "migrate", "nightly", "abc123")
	for _, w := range []string{"run jobs deploy migrate-nightly ", "--labels preview-pr=nightly ", "/previews/migrate:nightly-abc123", "run jobs execute migrate-nightly "} {
		if !strings.Contains(m.file(t, "calls"), w) {
			t.Errorf("migrate calls lack %q", w)
		}
	}
	env := m.file(t, "env-job-migrate-nightly")
	for _, w := range []string{"DEPLOY_ENV: nightly\n", "DOCUMENT_BUCKET: ascomply-preview-docs-nightly\n"} {
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

func TestPreviewTeardownNightlyEmptiesItsSlot(t *testing.T) {
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
		"storage rm gs://ascomply-preview-docs-nightly/**", "secrets delete nightly-gateway-token", "secrets delete nightly-s2s-token"}
	if got := strings.Join(deletes, "|"); got != strings.Join(want, "|") {
		t.Errorf("deletes = %v, want %v", deletes, want)
	}
	c := r.file(t, "calls")
	for _, w := range []string{"gs://ascomply-preview-nightly", "--filter labels.preview-pr=nightly ", "PR_DB=pr_0,ACTION=drop"} {
		if !strings.Contains(c, w) {
			t.Errorf("calls lack %q", w)
		}
	}
	if strings.Contains(c, "slot-1") || strings.Contains(c, "slot-2") {
		t.Errorf("the nightly's teardown reached a PR slot:\n%s", c)
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

func TestPreviewDatabasesMapsNames(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) { writeIn(t, dir, "list-databases", "postgres\npr_0\npr_672\npr_x\n") }, "databases")
	if want := "nightly\n672\n"; r.code != 0 || r.stdout != want {
		t.Fatalf("exit %d, databases = %q, want %q", r.code, r.stdout, want)
	}
}

func TestPreviewDatabasesFailsWhenGcloudFails(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) {
		writeIn(t, dir, "list-databases", "pr_672\n")
		writeIn(t, dir, "fail-sql-databases", "")
	}, "databases")
	if r.code == 0 {
		t.Fatalf("exit 0, stdout %q", r.stdout)
	}
}

func TestPreviewCapIgnoresDatabases(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) { writeIn(t, dir, "list-databases", "pr_9\n") }, "prs")
	if r.code != 0 || r.stdout != "" {
		t.Fatalf("exit %d, prs = %q, want empty", r.code, r.stdout)
	}
	r = runPreview(t, func(dir string) { writeIn(t, dir, "list-databases", "pr_9\n") }, "cap", "5")
	if !strings.Contains(r.stdout, "open=0") {
		t.Fatalf("cap = %q, want open=0", r.stdout)
	}
}

func TestPreviewTeardownFailsWhenTheDropFails(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) { writeIn(t, dir, "execfails", "1") }, "teardown", "7")
	if r.code == 0 || !strings.Contains(r.stderr, "left something behind") || strings.Count(r.file(t, "calls"), "run jobs execute preview-db") != 1 {
		t.Errorf("exit %d, stderr %q, calls %q", r.code, r.stderr, r.file(t, "calls"))
	}
}

var mailSecrets = []string{"nightly-gotrue-smtp-pass", "nightly-resend-sending-key", "nightly-resend-read-key", "nightly-resend-receiving-domain"}

const (
	blogTopicID   = "11111111-aaaa-4bbb-8ccc-222222222222"
	blogSegmentID = "33333333-aaaa-4bbb-8ccc-444444444444"
)

var blogIDEnv = []string{"RESEND_BLOG_TOPIC_ID=" + blogTopicID, "RESEND_BLOG_SEGMENT_ID=" + blogSegmentID}

func TestPreviewMailOnMountsOnlyTheNightlysMail(t *testing.T) {
	t.Parallel()
	r := runPreviewEnv(t, blogIDEnv, func(dir string) {
		writeIn(t, dir, "revisions-worker-nightly", "worker-nightly-00001-aaa\n")
		writeIn(t, dir, "latest-worker-nightly", "worker-nightly-00001-aaa\n")
	}, "mail-on", "nightly")
	if r.code != 0 {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	var lines []string
	for _, c := range nonBlankLines(r.file(t, "calls")) {
		if strings.HasPrefix(c, "run services update ") {
			lines = append(lines, c)
		}
	}
	if len(lines) != 4 {
		t.Fatalf("calls = %v, want four service updates", lines)
	}
	if !strings.Contains(r.file(t, "calls"), "run revisions list --service worker-nightly ") || r.exists("deleted") {
		t.Errorf("a lone serving revision must be listed and not deleted; calls = %q", r.file(t, "calls"))
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
	ids := "--update-env-vars RESEND_BLOG_TOPIC_ID=" + blogTopicID + ",RESEND_BLOG_SEGMENT_ID=" + blogSegmentID
	if !strings.HasPrefix(lines[2], "run services update notifications-nightly ") ||
		!strings.Contains(lines[2], "--update-secrets RESEND_SENDING_KEY=nightly-resend-sending-key:latest,RESEND_RECEIVING_DOMAIN=nightly-resend-receiving-domain:latest") ||
		!strings.Contains(lines[2], ids) {
		t.Errorf("notifications update = %q", lines[2])
	}
	if !strings.HasPrefix(lines[3], "run services update worker-nightly ") ||
		!strings.Contains(lines[3], "--update-secrets RESEND_RECEIVING_DOMAIN=nightly-resend-receiving-domain:latest") ||
		!strings.Contains(lines[3], ids) || strings.Contains(lines[3], "RESEND_SENDING_KEY") {
		t.Errorf("worker update = %q", lines[3])
	}
	if strings.Contains(r.file(t, "calls"), "versions access") {
		t.Error("mail-on read a secret value")
	}
}

func TestPreviewMailOnDeletesEveryWorkerRevisionButTheServingOne(t *testing.T) {
	t.Parallel()
	old := []string{"worker-nightly-00001-aaa", "worker-nightly-00002-bbb"}
	latest := "worker-nightly-00003-ccc"
	r := runPreviewEnv(t, blogIDEnv, func(dir string) {
		writeIn(t, dir, "revisions-worker-nightly", strings.Join(append(old, latest), "\n")+"\n")
		writeIn(t, dir, "latest-worker-nightly", latest+"\n")
	}, "mail-on", "nightly")
	if r.code != 0 {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	calls := nonBlankLines(r.file(t, "calls"))
	update := -1
	for i, c := range calls {
		if strings.HasPrefix(c, "run services update worker-nightly ") {
			update = i
		}
	}
	if update < 0 {
		t.Fatalf("no worker-nightly update in %v", calls)
	}
	for _, rev := range append(old, latest) {
		at := -1
		for i, c := range calls {
			if strings.HasPrefix(c, "run revisions delete "+rev+" ") {
				at = i
			}
		}
		switch {
		case rev == latest && at >= 0:
			t.Errorf("deleted the serving revision %s", rev)
		case rev != latest && at < update:
			t.Errorf("earlier revision %s not deleted after the worker update (delete at %d, update at %d)", rev, at, update)
		}
	}
}

func TestPreviewMailOnFailsWhenItCannotRetireTheOldWorkerRevisions(t *testing.T) {
	t.Parallel()
	old, latest := "worker-nightly-00001-aaa", "worker-nightly-00002-bbb"
	for _, tc := range []struct {
		name, want string
		setup      func(dir string)
	}{
		{"empty latest ready name", "no latest ready revision", func(dir string) { writeIn(t, dir, "latest-worker-nightly", "\n") }},
		{"describe fails", "cannot read the worker-nightly serving revision", func(dir string) { writeIn(t, dir, "faildescribe", "") }},
		{"list fails", "cannot list the worker-nightly revisions", func(dir string) { writeIn(t, dir, "fail-run-revisions", "") }},
		{"serving revision not listed", "is not in the worker-nightly revision list", func(dir string) { writeIn(t, dir, "revisions-worker-nightly", old+"\n") }},
		{"revision list empty", "is not in the worker-nightly revision list", func(dir string) { writeIn(t, dir, "revisions-worker-nightly", "") }},
		{"delete fails", "cannot delete the old worker-nightly revision " + old, func(dir string) { writeIn(t, dir, "faildelete", old+"\n") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := runPreviewEnv(t, blogIDEnv, func(dir string) {
				writeIn(t, dir, "revisions-worker-nightly", old+"\n"+latest+"\n")
				writeIn(t, dir, "latest-worker-nightly", latest+"\n")
				tc.setup(dir)
			}, "mail-on", "nightly")
			if r.code == 0 || !strings.Contains(r.stderr, tc.want) {
				t.Fatalf("exit %d, stderr %q, want a failure containing %q", r.code, r.stderr, tc.want)
			}
			if r.exists("deleted") {
				t.Errorf("deleted %q, want nothing", r.file(t, "deleted"))
			}
			for _, c := range nonBlankLines(r.file(t, "calls")) {
				if strings.HasPrefix(c, "run revisions delete ") && strings.Contains(c, latest) {
					t.Errorf("tried to delete the serving revision: %s", c)
				}
				if strings.HasPrefix(c, "run revisions ") && strings.Contains(c, "=") {
					t.Errorf("revision call carries a key=value argument: %s", c)
				}
			}
		})
	}
}

func TestPreviewMailOnRefusesMissingOrBadIds(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"RESEND_BLOG_TOPIC_ID", "RESEND_BLOG_SEGMENT_ID"} {
		for _, bad := range []string{"", "x"} {
			t.Run(name+"/"+bad, func(t *testing.T) {
				t.Parallel()
				env := []string{"RESEND_BLOG_TOPIC_ID=" + blogTopicID, "RESEND_BLOG_SEGMENT_ID=" + blogSegmentID, name + "=" + bad}
				r := runPreviewEnv(t, env, nil, "mail-on", "nightly")
				if r.code == 0 || !strings.Contains(r.stderr, name) || r.exists("calls") {
					t.Fatalf("exit %d, stderr %q, gcloud called %v", r.code, r.stderr, r.exists("calls"))
				}
			})
		}
	}
}

func blogListBody(items ...string) string {
	return `{"data":[` + strings.Join(items, ",") + `],"has_more":false}`
}

func blogItem(name, id string) string { return `{"id":"` + id + `","name":"` + name + `"}` }

func TestPreviewMailBlogIdsPrintsTheNamedIds(t *testing.T) {
	t.Parallel()
	r := runPreviewEnv(t, []string{"RESEND_READ_KEY=k-secret"}, func(dir string) {
		writeIn(t, dir, "curl-body-topics", blogListBody(blogItem("Marketing", "99999999-aaaa-4bbb-8ccc-000000000001"), blogItem("Blog", blogTopicID)))
		writeIn(t, dir, "curl-body-segments", blogListBody(blogItem("Registered", "99999999-aaaa-4bbb-8ccc-000000000002"), blogItem("Blog", blogSegmentID)))
	}, "mail-blog-ids", "nightly")
	want := "RESEND_BLOG_TOPIC_ID=" + blogTopicID + "\nRESEND_BLOG_SEGMENT_ID=" + blogSegmentID + "\n"
	if r.code != 0 || r.stdout != want {
		t.Fatalf("exit %d, stdout %q, want %q, stderr %q", r.code, r.stdout, want, r.stderr)
	}
	calls := r.file(t, "curl-calls")
	if strings.Contains(calls, "k-secret") || !strings.Contains(calls, "-H @-") ||
		!strings.Contains(calls, "https://api.resend.com/topics") || !strings.Contains(calls, "https://api.resend.com/segments") {
		t.Errorf("curl argv = %q", calls)
	}
	if !strings.Contains(r.file(t, "curl-stdin"), "Authorization: Bearer k-secret") {
		t.Error("the key did not reach curl on stdin")
	}
}

func TestPreviewMailBlogIdsFailsLoudly(t *testing.T) {
	t.Parallel()
	t.Run("no read key", func(t *testing.T) {
		t.Parallel()
		r := runPreview(t, nil, "mail-blog-ids", "nightly")
		if r.code == 0 || !strings.Contains(r.stderr, "RESEND_READ_KEY") || r.exists("curl-calls") {
			t.Fatalf("exit %d, stderr %q, curl called %v", r.code, r.stderr, r.exists("curl-calls"))
		}
	})
	ok := blogListBody(blogItem("Blog", blogTopicID))
	okSeg := blogListBody(blogItem("Blog", blogSegmentID))
	for _, c := range []struct {
		name         string
		files        map[string]string
		wantEndpoint string
		wantStatus   string
	}{
		{"no Blog topic", map[string]string{"curl-body-topics": blogListBody(blogItem("Marketing", blogTopicID)), "curl-body-segments": okSeg}, "/topics", ""},
		{"two Blog segments", map[string]string{"curl-body-topics": ok, "curl-body-segments": blogListBody(blogItem("Blog", blogSegmentID), blogItem("Blog", blogTopicID))}, "/segments", ""},
		{"HTTP 401", map[string]string{"curl-body-topics": `{"message":"body-secret","data":[` + blogItem("Blog", blogTopicID) + `]}`, "curl-code-topics": "401"}, "/topics", "401"},
		{"has_more without Blog", map[string]string{"curl-body-topics": `{"data":[` + blogItem("Marketing", blogTopicID) + `],"has_more":true}`, "curl-body-segments": okSeg}, "/topics", ""},
		{"id not a UUID", map[string]string{"curl-body-topics": ok, "curl-body-segments": blogListBody(blogItem("Blog", "not-a-uuid"))}, "/segments", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := runPreviewEnv(t, []string{"RESEND_READ_KEY=k-secret"}, func(dir string) {
				for n, b := range c.files {
					writeIn(t, dir, n, b)
				}
			}, "mail-blog-ids", "nightly")
			if r.code == 0 || !strings.Contains(r.stderr, c.wantEndpoint) || !strings.Contains(r.stderr, c.wantStatus) ||
				strings.Contains(r.stderr, "k-secret") || strings.Contains(r.stderr, "body-secret") {
				t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
			}
		})
	}
}

func TestPreviewMailCommandsRefuseAPR(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{"mail-preflight", "mail-on", "mail-off", "check-mail-on", "check-mail-off", "mail-read-key", "mail-domain", "mail-blog-ids"} {
		for _, bad := range []string{"7", ""} {
			t.Run(cmd+"/"+bad, func(t *testing.T) {
				t.Parallel()
				r := runPreviewEnv(t, append([]string{"RESEND_READ_KEY=k"}, blogIDEnv...), nil, cmd, bad)
				if r.code == 0 || r.exists("calls") || r.exists("curl-calls") {
					t.Fatalf("exit %d, gcloud called %v, curl called %v", r.code, r.exists("calls"), r.exists("curl-calls"))
				}
			})
		}
	}
}

func TestPreviewMailReadCommandsReadOneSecret(t *testing.T) {
	t.Parallel()
	const shimOutput = "shim-output"
	for cmd, name := range map[string]string{"mail-read-key": "nightly-resend-read-key", "mail-domain": "nightly-resend-receiving-domain", "mock-token": "nightly-mock-issuer-token"} {
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

// secretReads are the preview.sh subcommands that read one secret with gcloud.
var secretReads = []struct{ cmd, secret string }{
	{"contact-token", "nightly-hubspot-token"},
	{"mail-read-key", "nightly-resend-read-key"},
	{"mail-domain", "nightly-resend-receiving-domain"},
	{"mock-token", "nightly-mock-issuer-token"},
}

func TestPreviewSecretReadRetriesOneTransientFailure(t *testing.T) {
	t.Parallel()
	const value = "s3cret-value"
	for _, c := range secretReads {
		t.Run(c.cmd, func(t *testing.T) {
			t.Parallel()
			r := runPreview(t, func(dir string) {
				writeIn(t, dir, "failonce-secrets-versions", "")
				writeIn(t, dir, "access-"+c.secret, value)
			}, c.cmd, "nightly")
			calls := nonBlankLines(r.file(t, "calls"))
			if r.code != 0 || r.stdout != value || strings.Contains(r.stderr, value) || len(calls) != 2 {
				t.Fatalf("exit %d, stdout %q, stderr %q, calls %v", r.code, r.stdout, r.stderr, calls)
			}
			if got := orderOf(r); !slices.Equal(got, []string{"gcloud", "sleep 20", "gcloud"}) {
				t.Errorf("order %v, want a 20 s wait between the two tries", got)
			}
		})
	}
}

func TestPreviewSecretReadGivesUpAfterThreeTries(t *testing.T) {
	t.Parallel()
	for _, c := range secretReads {
		t.Run(c.cmd, func(t *testing.T) {
			t.Parallel()
			r := runPreview(t, func(dir string) { writeIn(t, dir, "fail-secrets-versions", "") }, c.cmd, "nightly")
			calls := nonBlankLines(r.file(t, "calls"))
			if r.code == 0 || len(calls) != 3 || !strings.Contains(r.stderr, "ERROR: gcloud secrets versions failed") || r.stdout != "" {
				t.Fatalf("exit %d, stdout %q, stderr %q, calls %v", r.code, r.stdout, r.stderr, calls)
			}
			if got := orderOf(r); !slices.Equal(got, []string{"gcloud", "sleep 20", "gcloud", "sleep 20", "gcloud"}) {
				t.Errorf("order %v, want a wait between tries and none after the last", got)
			}
		})
	}
}

func orderOf(r previewRun) []string {
	b, _ := os.ReadFile(filepath.Join(r.dir, "order"))
	return nonBlankLines(string(b))
}

func TestPreviewContactTokenRetryKeepsItsVersion(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) {
		writeIn(t, dir, "failonce-secrets-versions", "")
		writeIn(t, dir, "access-nightly-hubspot-token", "tok")
	}, "contact-token", "nightly", "7")
	calls := nonBlankLines(r.file(t, "calls"))
	if r.code != 0 || r.stdout != "tok" || len(calls) != 2 {
		t.Fatalf("exit %d, stdout %q, calls %v", r.code, r.stdout, calls)
	}
	for _, c := range calls {
		if !strings.HasPrefix(c, "secrets versions access 7 --secret nightly-hubspot-token ") {
			t.Errorf("call %q does not read version 7", c)
		}
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

func TestPreviewAuthKeepsTheImageLinkSettings(t *testing.T) {
	t.Parallel()
	rates := []string{"GOTRUE_RATE_LIMIT_VERIFY: '100000'", "GOTRUE_RATE_LIMIT_OTP: '100000'", "GOTRUE_RATE_LIMIT_TOKEN_REFRESH: '100000'"}
	banned := []string{"GOTRUE_MAILER_OTP_", "GOTRUE_RATE_LIMIT_HEADER"}
	check := func(label, body string) {
		for _, w := range rates {
			if !strings.Contains(body, w) {
				t.Errorf("%s lacks %q", label, w)
			}
		}
		for _, b := range banned {
			if strings.Contains(body, b) {
				t.Errorf("%s names %q", label, b)
			}
		}
	}
	check("PR 7 auth env", nameGoldens(t, "7")["env-auth"])
	check("nightly auth env", nameGoldens(t, "nightly")["env-auth"])
	on := runPreviewEnv(t, blogIDEnv, func(dir string) {
		writeIn(t, dir, "revisions-worker-nightly", "worker-nightly-00001-aaa\n")
		writeIn(t, dir, "latest-worker-nightly", "worker-nightly-00001-aaa\n")
	}, "mail-on", "nightly")
	off := runPreview(t, nil, "mail-off", "nightly")
	for label, r := range map[string]previewRun{"mail-on": on, "mail-off": off} {
		for _, b := range banned {
			if strings.Contains(r.file(t, "calls"), b) {
				t.Errorf("%s argv names %q", label, b)
			}
		}
	}
	if !strings.Contains(on.file(t, "calls"), "GOTRUE_MAILER_AUTOCONFIRM=false") {
		t.Errorf("mail-on argv lacks GOTRUE_MAILER_AUTOCONFIRM=false")
	}
}

func TestPreviewMailOffUnmountsAndTurnsAutoConfirmOn(t *testing.T) {
	t.Parallel()
	r := runPreview(t, nil, "mail-off", "nightly")
	if r.code != 0 {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	lines := nonBlankLines(r.file(t, "calls"))
	if len(lines) != 4 {
		t.Fatalf("calls = %v, want four service updates", lines)
	}
	if !strings.HasPrefix(lines[0], "run services update auth-nightly ") || !strings.Contains(lines[0], "--remove-secrets GOTRUE_SMTP_PASS") || !strings.Contains(lines[0], "GOTRUE_MAILER_AUTOCONFIRM=true") {
		t.Errorf("auth update = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "run services update tenancy-nightly ") || !strings.Contains(lines[1], "--remove-secrets RESEND_SENDING_KEY") {
		t.Errorf("tenancy update = %q", lines[1])
	}
	ids := "--remove-env-vars RESEND_BLOG_TOPIC_ID,RESEND_BLOG_SEGMENT_ID"
	if !strings.HasPrefix(lines[2], "run services update notifications-nightly ") || !strings.Contains(lines[2], "--remove-secrets RESEND_SENDING_KEY,RESEND_RECEIVING_DOMAIN") || !strings.Contains(lines[2], ids) {
		t.Errorf("notifications update = %q", lines[2])
	}
	if !strings.HasPrefix(lines[3], "run services update worker-nightly ") || !strings.Contains(lines[3], "--remove-secrets RESEND_RECEIVING_DOMAIN ") || !strings.Contains(lines[3], ids) {
		t.Errorf("worker update = %q", lines[3])
	}
}

func TestPreviewMailOffTriesEveryServiceAndFailsWhenOneFails(t *testing.T) {
	t.Parallel()
	services := []string{"auth-nightly", "tenancy-nightly", "notifications-nightly", "worker-nightly"}
	for _, failed := range services {
		t.Run(failed, func(t *testing.T) {
			t.Parallel()
			r := runPreview(t, func(dir string) { writeIn(t, dir, "failupdate", failed+"\n") }, "mail-off", "nightly")
			if r.code == 0 || !strings.Contains(r.stderr, "mail-off left real mail mounted") {
				t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
			}
			for _, svc := range services {
				if !strings.Contains(r.file(t, "calls"), "run services update "+svc+" ") {
					t.Errorf("%s was not tried after %s failed: %s", svc, failed, r.file(t, "calls"))
				}
			}
		})
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
	return runInvariantsWith(t, secrets, svcs, nil)
}

// runInvariantsWith is runInvariants with extra files in the shim dir (curl-body-<endpoint>, curl-code-<endpoint>).
// Every nightly-* secret is unreadable unless files sets denied.
func runInvariantsWith(t *testing.T, secrets []string, svcs []invSvc, files map[string]string) previewRun {
	t.Helper()
	return runInvariantsEnv(t, nil, secrets, svcs, files)
}

// runInvariantsEnv is runInvariantsWith with extra KEY=value environment entries.
func runInvariantsEnv(t *testing.T, env, secrets []string, svcs []invSvc, files map[string]string) previewRun {
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
	return runPreviewEnv(t, env, func(dir string) {
		writeIn(t, dir, "have", strings.Join(secrets, "\n")+"\n")
		writeIn(t, dir, "list-services", string(b))
		var denied []string
		for _, s := range secrets {
			if strings.HasPrefix(s, "nightly-") {
				denied = append(denied, s)
			}
		}
		writeIn(t, dir, "denied", strings.Join(denied, "\n")+"\n")
		for name, body := range files {
			writeIn(t, dir, name, body)
		}
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

func TestPreviewBlogIDRetriesA5xxButNotA401(t *testing.T) {
	t.Parallel()
	body := blogListBody(blogItem("Blog", blogTopicID))
	ok := runPreviewEnv(t, []string{"RESEND_READ_KEY=k"}, func(dir string) {
		writeIn(t, dir, "curl-body-topics", body)
		writeIn(t, dir, "curl-body-segments", blogListBody(blogItem("Blog", blogSegmentID)))
		writeIn(t, dir, "curl-seq-topics", "500\n200\n")
	}, "mail-blog-ids", "nightly")
	if ok.code != 0 || !strings.Contains(ok.stdout, "RESEND_BLOG_TOPIC_ID="+blogTopicID) {
		t.Fatalf("500 then 200: exit %d, stdout %q, stderr %q", ok.code, ok.stdout, ok.stderr)
	}
	if n := strings.Count(ok.file(t, "curl-calls"), "/topics"); n != 2 {
		t.Errorf("topics calls = %d, want 2", n)
	}
	bad := runPreviewEnv(t, []string{"RESEND_READ_KEY=k"}, func(dir string) {
		writeIn(t, dir, "curl-body-topics", body)
		writeIn(t, dir, "curl-seq-topics", "401\n200\n")
	}, "mail-blog-ids", "nightly")
	if bad.code == 0 || !strings.Contains(bad.stderr, "401") {
		t.Fatalf("401: exit %d, stderr %q", bad.code, bad.stderr)
	}
	if n := strings.Count(bad.file(t, "curl-calls"), "/topics"); n != 1 {
		t.Errorf("401 topics calls = %d, want 1", n)
	}
}

func TestPreviewCheckMailOnMountsOnlyTheWorkersSendingKeyAndDomain(t *testing.T) {
	t.Parallel()
	r := runPreview(t, nil, "check-mail-on", "nightly")
	if r.code != 0 {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	lines := nonBlankLines(r.file(t, "calls"))
	want := "--update-secrets RESEND_SENDING_KEY=nightly-resend-sending-key:latest,RESEND_RECEIVING_DOMAIN=nightly-resend-receiving-domain:latest"
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "run services update worker-nightly ") || !strings.Contains(lines[0], want) {
		t.Fatalf("calls = %v", lines)
	}
	if strings.Contains(r.file(t, "calls"), "versions access") {
		t.Errorf("check-mail-on read a secret value")
	}
}

func TestPreviewCheckMailOffUnmountsTheWorkersMail(t *testing.T) {
	t.Parallel()
	r := runPreview(t, nil, "check-mail-off", "nightly")
	if r.code != 0 {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	lines := nonBlankLines(r.file(t, "calls"))
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "run services update worker-nightly ") || !strings.Contains(lines[0], "--remove-secrets RESEND_SENDING_KEY,RESEND_RECEIVING_DOMAIN") {
		t.Fatalf("calls = %v", lines)
	}
}

func TestPreviewCheckMailOffFailsWhenTheUpdateFails(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) { writeIn(t, dir, "failupdate", "worker-nightly\n") }, "check-mail-off", "nightly")
	if r.code == 0 || !strings.Contains(r.stderr, "check-mail-off left real mail mounted") {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
}

func TestPreviewCheckMailOnFailsWhenTheUpdateFails(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) { writeIn(t, dir, "failupdate", "worker-nightly\n") }, "check-mail-on", "nightly")
	if r.code == 0 {
		t.Fatalf("exit 0, stderr %q", r.stderr)
	}
}

// A preview-db or grant execution is retried only when its log shows SQLSTATE 53300.
func TestPreviewJobRetriesOnlyOnTooManyConnections(t *testing.T) {
	t.Parallel()
	const full = `psql: error: connection to server at "10.20.0.3", port 5432 failed: FATAL:  remaining connection slots are reserved for roles with privileges of the "pg_use_reserved_connections" role`
	for _, c := range []struct {
		name, fails, logs string
		args              []string
		wantOK            bool
		wantRuns          int
	}{
		{"database: 53300 once, then success", "1", full, []string{"database", "7"}, true, 2},
		{"grant: pgx 53300 once, then success", "1", "failed to connect: FATAL: sorry, too many clients already (SQLSTATE 53300)", []string{"grant", "7"}, true, 2},
		{"database: 53300 every time stops after 3 tries", "9", full, []string{"database", "7"}, false, 3},
		{"database: any other failure is not retried", "1", "ERROR:  syntax error", []string{"database", "7"}, false, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := runPreview(t, func(dir string) {
				writeIn(t, dir, "execfails", c.fails)
				writeIn(t, dir, "logs", c.logs)
			}, c.args...)
			runs := strings.Count(r.file(t, "calls"), "run jobs execute ")
			if (r.code == 0) != c.wantOK || runs != c.wantRuns {
				t.Errorf("exit %d after %d executions, want ok=%v after %d; stderr %q", r.code, runs, c.wantOK, c.wantRuns, r.stderr)
			}
			if c.wantRuns > 1 && !strings.Contains(r.file(t, "order"), "sleep 30") {
				t.Errorf("no 30 s backoff before the retry: %q", r.file(t, "order"))
			}
		})
	}
}

// imagesWritePerms are the six permissions that let a caller replace or remove a production image.
var imagesWritePerms = []string{
	"artifactregistry.repositories.uploadArtifacts",
	"artifactregistry.tags.create",
	"artifactregistry.tags.update",
	"artifactregistry.tags.delete",
	"artifactregistry.versions.delete",
	"artifactregistry.repositories.deleteArtifacts",
}

const (
	imagesEP = "images:testIamPermissions"
	prodEP   = "ascomply-prod:testIamPermissions"
	imgHeld  = "Preview invariants hold"
)

// prodImagesRepo is the repository deploy-prod.yml deploys from: the last path segment of its REGISTRY.
func prodImagesRepo(t *testing.T) string {
	t.Helper()
	wf, err := os.ReadFile(filepath.Join(repoRoot(t), ".github/workflows/deploy-prod.yml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^\s*REGISTRY:\s*(\S+)`).FindSubmatch(wf)
	if m == nil {
		t.Fatal("no REGISTRY in deploy-prod.yml")
	}
	reg := string(m[1])
	repo := reg[strings.LastIndex(reg, "/")+1:]
	if repo == "" {
		t.Fatalf("REGISTRY %q has no repository segment", reg)
	}
	return repo
}

func imgInvariants(t *testing.T, files map[string]string) previewRun {
	t.Helper()
	return runInvariantsWith(t, []string{nlKeySecret}, nlGoodSvcs(), files)
}

// imgCalls are the logged curl calls whose last argument ends in suffix.
func imgCalls(t *testing.T, r previewRun, suffix string) []string {
	t.Helper()
	var out []string
	for _, l := range strings.Split(r.file(t, "curl-calls"), "\n") {
		if f := strings.Fields(l); len(f) > 0 && strings.HasSuffix(f[len(f)-1], suffix) {
			out = append(out, l)
		}
	}
	return out
}

// errLines returns the ::error:: lines of both streams that contain every want.
func errLines(r previewRun, want ...string) []string { return annLines(r, "::error::", want...) }

// annLines is errLines for any annotation prefix, such as ::warning::.
func annLines(r previewRun, prefix string, want ...string) []string {
	var out []string
next:
	for _, l := range strings.Split(r.stdout+"\n"+r.stderr, "\n") {
		if !strings.Contains(l, prefix) {
			continue
		}
		for _, w := range want {
			if !strings.Contains(l, w) {
				continue next
			}
		}
		out = append(out, l)
	}
	return out
}

func TestPreviewInvariantsRefuseWriteOnTheProductionImages(t *testing.T) {
	t.Parallel()
	repo := prodImagesRepo(t)
	for _, p := range imagesWritePerms {
		t.Run(p, func(t *testing.T) {
			t.Parallel()
			r := imgInvariants(t, map[string]string{"curl-body-" + imagesEP: `{"permissions":["` + p + `"]}`})
			if r.code != 1 {
				t.Fatalf("invariants exited %d, want 1:\n%s\n%s", r.code, r.stdout, r.stderr)
			}
			if len(errLines(r, p, " on "+repo+".")) == 0 {
				t.Errorf("no ::error:: line names %s on %s:\n%s\n%s", p, repo, r.stdout, r.stderr)
			}
			if strings.Contains(r.stdout, imgHeld) {
				t.Errorf("success line printed:\n%s", r.stdout)
			}
		})
	}
}

func TestPreviewInvariantsAskTheProductionRepositoryForEveryWritePermission(t *testing.T) {
	t.Parallel()
	repo := prodImagesRepo(t)
	r := imgInvariants(t, nil)
	if r.code != 0 {
		t.Fatalf("invariants exited %d:\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	url := "https://artifactregistry.googleapis.com/v1/projects/ascomply-preview/locations/africa-south1/repositories/" + repo + ":testIamPermissions"
	calls := imgCalls(t, r, ":testIamPermissions")
	var asked []string
	for _, c := range calls {
		if strings.HasSuffix(c, url) {
			asked = append(asked, c)
		}
	}
	if len(asked) != 1 {
		t.Fatalf("want one call to %s, got %d in:\n%s", url, len(asked), strings.Join(calls, "\n"))
	}
	body := flagValue(asked[0], "-d")
	for _, p := range imagesWritePerms {
		if !strings.Contains(body, p) {
			t.Errorf("-d body %q lacks %s", body, p)
		}
	}
	if len(imgCalls(t, r, "/ascomply-prod:testIamPermissions")) != 1 {
		t.Errorf("the ascomply-prod call is gone:\n%s", strings.Join(calls, "\n"))
	}
}

func TestPreviewInvariantsPassWithNoProductionImageWrite(t *testing.T) {
	t.Parallel()
	r := imgInvariants(t, map[string]string{"curl-body-" + imagesEP: `{}`, "curl-body-" + prodEP: `{}`})
	if r.code != 0 {
		t.Fatalf("invariants exited %d:\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stdout, imgHeld) || !strings.Contains(r.stdout, "no write on the production image repository") {
		t.Errorf("success line does not name the production image repository:\n%s", r.stdout)
	}
	if len(imgCalls(t, r, "/"+prodImagesRepo(t)+":testIamPermissions")) != 1 {
		t.Errorf("the production image repository was never asked:\n%s", r.file(t, "curl-calls"))
	}
}

func TestPreviewInvariantsNameEveryHeldImagePermissionAndPassOnAnEmptySet(t *testing.T) {
	t.Parallel()
	r := imgInvariants(t, map[string]string{"curl-body-" + imagesEP: `{"permissions":["artifactregistry.tags.create","artifactregistry.versions.delete"]}`})
	if r.code != 1 || len(errLines(r, "artifactregistry.tags.create", "artifactregistry.versions.delete", prodImagesRepo(t))) != 1 {
		t.Errorf("exit %d; want exit 1 and one ::error:: line naming both held permissions:\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	// An empty permission list is the same as none held.
	r = imgInvariants(t, map[string]string{"curl-body-" + imagesEP: `{"permissions":[]}`})
	if r.code != 0 || !strings.Contains(r.stdout, imgHeld) {
		t.Errorf("empty permission list: exit %d:\n%s\n%s", r.code, r.stdout, r.stderr)
	}
}

func TestPreviewInvariantsFailOnAnUnexpectedImageRepositoryAnswer(t *testing.T) {
	t.Parallel()
	for _, code := range []string{"403", "404", "500", "000", "401", "429", "302", "201"} {
		t.Run(code, func(t *testing.T) {
			t.Parallel()
			r := imgInvariants(t, map[string]string{"curl-code-" + imagesEP: code})
			if r.code == 0 {
				t.Fatalf("an answer of HTTP %s passed:\n%s\n%s", code, r.stdout, r.stderr)
			}
			if len(errLines(r, "HTTP "+code)) == 0 {
				t.Errorf("no ::error:: line names HTTP %s:\n%s\n%s", code, r.stdout, r.stderr)
			}
			if strings.Contains(r.stdout, imgHeld) {
				t.Errorf("success line printed:\n%s", r.stdout)
			}
		})
	}
}

func TestPreviewInvariantsReportBothIAMProblems(t *testing.T) {
	t.Parallel()
	repo := prodImagesRepo(t)
	r := imgInvariants(t, map[string]string{
		"curl-body-" + prodEP:   `{"permissions":["run.services.get"]}`,
		"curl-body-" + imagesEP: `{"permissions":["artifactregistry.tags.update"]}`,
	})
	if r.code != 1 {
		t.Fatalf("invariants exited %d, want 1:\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	if len(errLines(r, "run.services.get", "ascomply-prod")) == 0 {
		t.Errorf("the ascomply-prod problem is not reported:\n%s\n%s", r.stdout, r.stderr)
	}
	if len(errLines(r, "artifactregistry.tags.update", repo)) == 0 {
		t.Errorf("the image repository problem is not reported:\n%s\n%s", r.stdout, r.stderr)
	}
}

const (
	deployNightlySA = "deploy-nightly@ascomply-preview.iam.gserviceaccount.com"
	invSentinel     = "SENTINEL-VALUE-1"
	invClosed       = "NIGHTLY_KEYS_CLOSED=true"
	invWarnTail     = "(not enforced: NIGHTLY_KEYS_CLOSED is not true)"
)

func nightlyIdentityEP(email string) string { return email + ":testIamPermissions" }

// nightlyPerms are the two permissions the preview deployer must not hold on a nightly identity.
var nightlyPerms = []string{"iam.serviceAccounts.actAs", "iam.serviceAccounts.getAccessToken"}

func permBody(perm string) string { return `{"permissions":["` + perm + `"]}` }

func TestPreviewInvariantsRefuseAReadableNightlySecret(t *testing.T) {
	t.Parallel()
	r := runInvariantsEnv(t, []string{invClosed}, []string{"nightly-hubspot-token", "nightly-resend-read-key", "pr-7-gateway-token"}, nlGoodSvcs(),
		map[string]string{"denied": "nightly-resend-read-key\n", "access-nightly-hubspot-token": invSentinel})
	if r.code != 1 {
		t.Errorf("invariants exited %d, want 1:\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	if len(errLines(r, "the preview deployer can read nightly-hubspot-token")) != 1 {
		t.Errorf("want one ::error:: line naming nightly-hubspot-token:\n%s\n%s", r.stdout, r.stderr)
	}
	for _, l := range strings.Split(r.stdout+"\n"+r.stderr, "\n") {
		if strings.Contains(l, "nightly-resend-read-key") {
			t.Errorf("a denied secret is named: %q", l)
		}
	}
	if strings.Contains(r.stdout+r.stderr, invSentinel) {
		t.Errorf("a secret value was printed:\n%s\n%s", r.stdout, r.stderr)
	}
}

func TestPreviewInvariantsWarnOnAReadableNightlySecretUntilClosed(t *testing.T) {
	t.Parallel()
	for name, env := range map[string][]string{"unset": nil, "false": {"NIGHTLY_KEYS_CLOSED=false"}, "empty": {"NIGHTLY_KEYS_CLOSED="}, "other": {"NIGHTLY_KEYS_CLOSED=1"}, "case": {"NIGHTLY_KEYS_CLOSED=True"}, "upper": {"NIGHTLY_KEYS_CLOSED=TRUE"}, "space": {"NIGHTLY_KEYS_CLOSED= true"}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := runInvariantsEnv(t, env, []string{"nightly-hubspot-token", "pr-7-gateway-token"}, nlGoodSvcs(),
				map[string]string{"denied": "", "access-nightly-hubspot-token": invSentinel})
			if r.code != 0 {
				t.Fatalf("invariants exited %d, want 0:\n%s\n%s", r.code, r.stdout, r.stderr)
			}
			if n := len(annLines(r, "::warning::", "the preview deployer can read nightly-hubspot-token")); n != 1 {
				t.Errorf("%d ::warning:: lines name nightly-hubspot-token, want 1:\n%s\n%s", n, r.stdout, r.stderr)
			}
			if len(errLines(r)) != 0 {
				t.Errorf("::error:: line in warn mode:\n%s\n%s", r.stdout, r.stderr)
			}
			if !strings.HasSuffix(strings.TrimSpace(r.stdout), invWarnTail) {
				t.Errorf("stdout does not end %q:\n%s", invWarnTail, r.stdout)
			}
			if strings.Contains(r.stdout, "no read of a nightly-* secret") || !strings.Contains(r.stdout, "not enforced and 1 are readable") {
				t.Errorf("success line misstates the nightly-secret read:\n%s", r.stdout)
			}
			if strings.Contains(r.stdout+r.stderr, invSentinel) {
				t.Errorf("a secret value was printed:\n%s\n%s", r.stdout, r.stderr)
			}
		})
	}
}

func TestPreviewInvariantsNameTheAccountWhenCurlFails(t *testing.T) {
	t.Parallel()
	r := runInvariantsWith(t, []string{"pr-7-gateway-token"}, nlGoodSvcs(), map[string]string{
		"curl-exit-" + nightlyIdentityEP(runNightlySA):    "7",
		"curl-body-" + nightlyIdentityEP(deployNightlySA): permBody("iam.serviceAccounts.getAccessToken"),
	})
	if r.code != 1 {
		t.Errorf("invariants exited %d, want 1:\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	if len(errLines(r, "run-nightly@", "7")) != 1 {
		t.Errorf("want one ::error:: line naming run-nightly and curl exit 7:\n%s\n%s", r.stdout, r.stderr)
	}
	if len(errLines(r, "iam.serviceAccounts.getAccessToken", "deploy-nightly@")) != 1 {
		t.Errorf("the check on deploy-nightly did not run after the curl failure:\n%s\n%s", r.stdout, r.stderr)
	}
}

func TestPreviewInvariantsWarnModeStillRefusesActAs(t *testing.T) {
	t.Parallel()
	for name, env := range map[string][]string{"unset": nil, "false": {"NIGHTLY_KEYS_CLOSED=false"}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := runInvariantsEnv(t, env, []string{"nightly-hubspot-token"}, nlGoodSvcs(),
				map[string]string{"curl-body-" + nightlyIdentityEP(runNightlySA): permBody("iam.serviceAccounts.actAs")})
			if r.code != 1 {
				t.Errorf("invariants exited %d, want 1:\n%s\n%s", r.code, r.stdout, r.stderr)
			}
			if len(errLines(r, "iam.serviceAccounts.actAs", "run-nightly@")) != 1 {
				t.Errorf("want one ::error:: line naming actAs and run-nightly:\n%s\n%s", r.stdout, r.stderr)
			}
		})
	}
}

func TestPreviewInvariantsReadOnlyNightlySecrets(t *testing.T) {
	t.Parallel()
	// The last four are lookalikes: readable, so a loose prefix match would read them.
	have := []string{"nightly-hubspot-token", "pr-7-gateway-token", "nightly-resend-read-key", "pr-7-s2s-token", "app-password", "not-nightly-x", "nightly", "Nightly-x", "pr-7-nightly-token"}
	r := runInvariantsWith(t, have, nlGoodSvcs(), nil)
	if r.code != 0 {
		t.Fatalf("invariants exited %d:\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	var read []string
	for _, l := range nonBlankLines(r.file(t, "calls")) {
		if strings.HasPrefix(l, "secrets versions access ") {
			read = append(read, "access latest --secret "+flagValue(l, "--secret"))
		}
	}
	sort.Strings(read)
	want := []string{"access latest --secret nightly-hubspot-token", "access latest --secret nightly-resend-read-key"}
	if !slices.Equal(read, want) {
		t.Errorf("secret reads = %v, want %v", read, want)
	}
	for _, l := range nonBlankLines(r.file(t, "calls")) {
		if strings.HasPrefix(l, "secrets versions access ") && !strings.HasPrefix(l, "secrets versions access latest --secret ") {
			t.Errorf("read is not of the latest version: %q", l)
		}
	}
}

func TestPreviewInvariantsRefuseActAsOnANightlyIdentity(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"", invClosed} {
		for _, email := range []string{runNightlySA, deployNightlySA} {
			for _, p := range nightlyPerms {
				t.Run(strings.TrimPrefix(mode, "NIGHTLY_KEYS_CLOSED=")+"/"+email+"/"+p, func(t *testing.T) {
					t.Parallel()
					var env []string
					if mode != "" {
						env = []string{mode}
					}
					r := runInvariantsEnv(t, env, []string{nlKeySecret}, nlGoodSvcs(), map[string]string{"curl-body-" + nightlyIdentityEP(email): permBody(p)})
					if r.code != 1 {
						t.Fatalf("invariants exited %d, want 1:\n%s\n%s", r.code, r.stdout, r.stderr)
					}
					if len(errLines(r, p, email)) != 1 {
						t.Errorf("want one ::error:: line naming %s and %s:\n%s\n%s", p, email, r.stdout, r.stderr)
					}
					if strings.Contains(r.stdout, imgHeld) {
						t.Errorf("success line printed:\n%s", r.stdout)
					}
				})
			}
		}
	}
}

func TestPreviewInvariantsAskBothNightlyIdentities(t *testing.T) {
	t.Parallel()
	r := runInvariantsWith(t, []string{nlKeySecret}, nlGoodSvcs(), nil)
	if r.code != 0 {
		t.Fatalf("invariants exited %d:\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	for _, email := range []string{runNightlySA, deployNightlySA} {
		url := "https://iam.googleapis.com/v1/projects/ascomply-preview/serviceAccounts/" + nightlyIdentityEP(email)
		var asked []string
		for _, c := range imgCalls(t, r, nightlyIdentityEP(email)) {
			if strings.HasSuffix(c, url) {
				asked = append(asked, c)
			}
		}
		if len(asked) != 1 {
			t.Errorf("want one call to %s, got %d in:\n%s", url, len(asked), r.file(t, "curl-calls"))
			continue
		}
		body := flagValue(asked[0], "-d")
		for _, p := range nightlyPerms {
			if !strings.Contains(body, p) {
				t.Errorf("-d body %q lacks %s", body, p)
			}
		}
	}
}

func TestPreviewInvariantsPassWhenTheNightlyIsClosed(t *testing.T) {
	t.Parallel()
	r := runInvariantsEnv(t, []string{invClosed}, []string{"nightly-hubspot-token", "nightly-resend-read-key", "pr-7-gateway-token"}, nlGoodSvcs(), nil)
	if r.code != 0 {
		t.Fatalf("invariants exited %d:\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	for _, w := range []string{imgHeld, "no act-as on run-nightly or deploy-nightly", "no read of a nightly-* secret"} {
		if !strings.Contains(r.stdout, w) {
			t.Errorf("success line lacks %q:\n%s", w, r.stdout)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(r.stdout), "(enforced)") || strings.Contains(r.stdout, "not enforced") {
		t.Errorf("stdout does not end (enforced):\n%s", r.stdout)
	}
	if len(annLines(r, "::warning::")) != 0 {
		t.Errorf("warning on a closed nightly:\n%s\n%s", r.stdout, r.stderr)
	}
}

func TestPreviewInvariantsFailClosedOnAnUnexpectedReadError(t *testing.T) {
	t.Parallel()
	for name, env := range map[string][]string{"unset": nil, "closed": {invClosed}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := runInvariantsEnv(t, env, []string{"nightly-hubspot-token", "pr-7-gateway-token"}, nlGoodSvcs(),
				map[string]string{"denied": "", "accesserr": "nightly-hubspot-token\n"})
			if r.code == 0 {
				t.Fatalf("a read error passed:\n%s\n%s", r.stdout, r.stderr)
			}
			if len(errLines(r, "could not check nightly-hubspot-token", "INTERNAL")) != 1 {
				t.Errorf("want one ::error:: line with the secret name and the error:\n%s\n%s", r.stdout, r.stderr)
			}
			if strings.Contains(r.stdout, imgHeld) {
				t.Errorf("success line printed:\n%s", r.stdout)
			}
		})
	}
}

func TestPreviewInvariantsFailClosedOnAnIdentityAnswer(t *testing.T) {
	t.Parallel()
	for _, email := range []string{runNightlySA, deployNightlySA} {
		for _, code := range []string{"403", "404", "500", "000", "401", "429", "302", "201"} {
			t.Run(email+"/"+code, func(t *testing.T) {
				t.Parallel()
				r := runInvariantsWith(t, []string{nlKeySecret}, nlGoodSvcs(), map[string]string{"curl-code-" + nightlyIdentityEP(email): code})
				if r.code == 0 {
					t.Fatalf("an answer of HTTP %s passed:\n%s\n%s", code, r.stdout, r.stderr)
				}
				if len(errLines(r, email, "HTTP "+code)) == 0 {
					t.Errorf("no ::error:: line names %s and HTTP %s:\n%s\n%s", email, code, r.stdout, r.stderr)
				}
				if strings.Contains(r.stdout, imgHeld) {
					t.Errorf("success line printed:\n%s", r.stdout)
				}
			})
		}
	}
}

func TestPreviewInvariantsReportTheNightlyAndTheImageProblemsTogether(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"denied": "",
		"curl-body-" + nightlyIdentityEP(runNightlySA): permBody("iam.serviceAccounts.actAs"),
		"curl-body-" + imagesEP:                        permBody("artifactregistry.tags.update"),
	}
	have := []string{"nightly-hubspot-token", "pr-7-gateway-token"}
	r := runInvariantsEnv(t, []string{invClosed}, have, nlGoodSvcs(), files)
	if r.code != 1 || len(errLines(r)) != 3 {
		t.Errorf("enforced: exit %d with %d ::error:: lines, want exit 1 and 3:\n%s\n%s", r.code, len(errLines(r)), r.stdout, r.stderr)
	}
	for _, w := range [][]string{{"nightly-hubspot-token"}, {"iam.serviceAccounts.actAs", "run-nightly@"}, {"artifactregistry.tags.update", prodImagesRepo(t)}} {
		if len(errLines(r, w...)) != 1 {
			t.Errorf("want one ::error:: line with %v:\n%s\n%s", w, r.stdout, r.stderr)
		}
	}
	// Both nightly identities are reported in one run, not just the first.
	both := map[string]string{
		"curl-body-" + nightlyIdentityEP(runNightlySA):    permBody("iam.serviceAccounts.actAs"),
		"curl-body-" + nightlyIdentityEP(deployNightlySA): permBody("iam.serviceAccounts.getAccessToken"),
	}
	b := runInvariantsEnv(t, []string{invClosed}, have, nlGoodSvcs(), both)
	if len(errLines(b, "iam.serviceAccounts.actAs", "run-nightly@")) != 1 || len(errLines(b, "iam.serviceAccounts.getAccessToken", "deploy-nightly@")) != 1 {
		t.Errorf("want one ::error:: line per held nightly identity:\n%s\n%s", b.stdout, b.stderr)
	}
	// Soft mode: the read is a warning, and the other two problems still fail the run.
	s := runInvariantsEnv(t, nil, have, nlGoodSvcs(), files)
	if s.code != 1 || len(errLines(s)) != 2 || len(annLines(s, "::warning::", "nightly-hubspot-token")) != 1 {
		t.Errorf("soft: exit %d, %d ::error:: lines, %d warnings; want exit 1, 2 errors, 1 warning:\n%s\n%s",
			s.code, len(errLines(s)), len(annLines(s, "::warning::", "nightly-hubspot-token")), s.stdout, s.stderr)
	}
}

const (
	runNightlySA = "run-nightly@ascomply-preview.iam.gserviceaccount.com"
	runPreviewSA = "run-preview@ascomply-preview.iam.gserviceaccount.com"
)

func nightlyBindLine(secret string) string {
	return "secrets add-iam-policy-binding " + secret + " --project ascomply-preview --member serviceAccount:" + runNightlySA +
		" --role roles/secretmanager.secretAccessor --condition=None"
}

// nightlySecretNames is every secret `secrets nightly` makes.
func nightlySecretNames(t *testing.T) []string {
	t.Helper()
	return previewList(t, "PR_SECRETS")
}

func TestPreviewNightlyRunsAsRunNightly(t *testing.T) {
	t.Parallel()
	env := []string{"NIGHTLY_HUBSPOT_TOKEN_VERSION=7"}
	svcs := prAll(t)
	dep := runPreviewEnv(t, env, nil, append([]string{"deploy", "nightly", "abc123"}, svcs...)...)
	mig := runPreviewEnv(t, env, nil, "migrate", "nightly", "abc123")
	if dep.code != 0 || mig.code != 0 {
		t.Fatalf("deploy exit %d (%s), migrate exit %d (%s)", dep.code, dep.stderr, mig.code, mig.stderr)
	}
	n := 0
	for _, l := range append(nonBlankLines(dep.file(t, "calls")), nonBlankLines(mig.file(t, "calls"))...) {
		if !strings.HasPrefix(l, "run deploy ") && !strings.HasPrefix(l, "run jobs deploy ") {
			continue
		}
		n++
		if got := flagValue(l, "--service-account"); got != runNightlySA {
			t.Errorf("--service-account = %q, want %q:\n%s", got, runNightlySA, l)
		}
	}
	if n != len(svcs)+1 {
		t.Errorf("%d deploy lines, want %d services + the migrate job", n, len(svcs))
	}
}

func TestPreviewPRNeverRunsAsRunNightly(t *testing.T) {
	t.Parallel()
	svcs := prAll(t)
	var all []string
	for _, args := range [][]string{append([]string{"deploy", "7", "abc123"}, svcs...), {"migrate", "7", "abc123"}, {"secrets", "7"}, {"teardown", "7"}} {
		r := runPreview(t, nil, args...)
		if r.code != 0 {
			t.Fatalf("%s exited %d: %s", args[0], r.code, r.stderr)
		}
		all = append(all, nonBlankLines(r.file(t, "calls"))...)
	}
	sa := 0
	for _, l := range all {
		if strings.Contains(l, "run-nightly") {
			t.Errorf("a PR call names run-nightly:\n%s", l)
		}
		if got := flagValue(l, "--service-account"); got != "" {
			sa++
			if got != runPreviewSA {
				t.Errorf("--service-account = %q, want %q", got, runPreviewSA)
			}
		}
	}
	if sa != len(svcs)+1 {
		t.Errorf("%d --service-account flags, want %d", sa, len(svcs)+1)
	}
}

func TestPreviewSecretsNightlyBindsRunNightlyOnEachSecret(t *testing.T) {
	t.Parallel()
	r := runPreview(t, nil, "secrets", "nightly")
	if r.code != 0 {
		t.Fatalf("secrets exited %d: %s", r.code, r.stderr)
	}
	lines := nonBlankLines(r.file(t, "calls"))
	names := nightlySecretNames(t)
	if len(names) != 13 {
		t.Fatalf("%d nightly secrets, want 13", len(names))
	}
	for _, n := range names {
		sec := "nightly-" + n
		create, bind, count := -1, -1, 0
		for i, l := range lines {
			if strings.HasPrefix(l, "secrets create "+sec+" ") {
				create = i
			}
			if l == nightlyBindLine(sec) {
				bind = i
				count++
			}
		}
		if count != 1 {
			t.Errorf("%s: %d binding lines, want 1", sec, count)
		} else if create < 0 || bind < create {
			t.Errorf("%s: binding (line %d) is not after its create (line %d)", sec, bind, create)
		}
	}
	for _, l := range lines {
		if strings.Contains(l, "add-iam-policy-binding") && strings.Contains(l, "run-preview") {
			t.Errorf("a binding names run-preview:\n%s", l)
		}
	}
}

func TestPreviewSecretsNightlyBindsAnExistingSecret(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) {
		writeIn(t, dir, "have-nightly", "nightly-gateway-token\nnightly-database-app-url\n")
	}, "secrets", "nightly")
	if r.code != 0 {
		t.Fatalf("secrets exited %d: %s", r.code, r.stderr)
	}
	calls := r.file(t, "calls")
	if got := strings.Count(calls, "secrets create nightly-"); got != 11 {
		t.Errorf("%d creates, want 11 (the 13 secrets less the 2 that exist)", got)
	}
	for _, sec := range []string{"nightly-gateway-token", "nightly-database-app-url"} {
		if strings.Contains(calls, "secrets create "+sec+" ") {
			t.Errorf("%s exists but was created", sec)
		}
		if got := strings.Count(calls, nightlyBindLine(sec)+"\n"); got != 1 {
			t.Errorf("%s: %d binding lines, want 1", sec, got)
		}
	}
}

func TestPreviewSecretsNightlyFailsWhenABindingFails(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) { writeIn(t, dir, "fail-secrets-add-iam-policy-binding", "") }, "secrets", "nightly")
	calls := r.file(t, "calls")
	if got := strings.Count(calls, "secrets create nightly-"); got != 1 {
		t.Errorf("%d creates before the stop, want 1", got)
	}
	if r.code == 0 {
		t.Errorf("secrets nightly exited 0 after a failed binding")
	}
	if !strings.Contains(r.stderr, "::error::cannot give run-nightly access to nightly-") {
		t.Errorf("stderr lacks the binding error:\n%s", r.stderr)
	}
	if got := strings.Count(calls, "secrets add-iam-policy-binding"); got != 1 {
		t.Errorf("%d binding attempts, want 1 (the run stops at the first failure)", got)
	}
}

func TestPreviewSecretsPRMakesNoIAMCall(t *testing.T) {
	t.Parallel()
	for name, setup := range map[string]func(dir string){
		"new":      nil,
		"existing": func(dir string) { writeIn(t, dir, "have", allPRSecrets(t)) },
	} {
		r := runPreview(t, setup, "secrets", "7")
		if r.code != 0 {
			t.Fatalf("%s: secrets exited %d: %s", name, r.code, r.stderr)
		}
		calls := r.file(t, "calls")
		if name == "new" && !strings.Contains(calls, "secrets create pr-7-") {
			t.Errorf("%s: no secret was created:\n%s", name, calls)
		}
		if strings.Contains(calls, "add-iam-policy-binding") {
			t.Errorf("%s: a PR secret got an IAM call:\n%s", name, calls)
		}
	}
}

func TestPreviewInvariantsPassWhenANightlySecretVanishes(t *testing.T) {
	t.Parallel()
	for name, env := range map[string][]string{"unset": nil, "closed": {invClosed}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := runInvariantsEnv(t, env, []string{"nightly-hubspot-token", "nightly-resend-read-key"}, nlGoodSvcs(),
				map[string]string{"denied": "nightly-resend-read-key\n", "missing": "nightly-hubspot-token\n"})
			if r.code != 0 || len(errLines(r)) != 0 {
				t.Fatalf("exit %d with ::error:: lines, want a pass:\n%s\n%s", r.code, r.stdout, r.stderr)
			}
			if len(annLines(r, "::notice::", "nightly-hubspot-token")) != 1 {
				t.Errorf("want one ::notice:: naming nightly-hubspot-token:\n%s\n%s", r.stdout, r.stderr)
			}
			if !strings.Contains(r.stdout, imgHeld) {
				t.Errorf("no success line:\n%s", r.stdout)
			}
		})
	}
}

func TestPreviewInvariantsReportAnIdentityAnswerAndRunTheLaterChecks(t *testing.T) {
	t.Parallel()
	r := runInvariantsWith(t, []string{nlKeySecret}, nlGoodSvcs(), map[string]string{
		"curl-code-" + nightlyIdentityEP(runNightlySA):    "500",
		"curl-body-" + nightlyIdentityEP(deployNightlySA): permBody("iam.serviceAccounts.actAs"),
		"enabled-services": "cloudbuild.googleapis.com\n",
	})
	if r.code != 1 {
		t.Fatalf("invariants exited %d, want 1:\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	if len(errLines(r, "run-nightly@", "HTTP 500")) != 1 {
		t.Errorf("want one ::error:: naming run-nightly and HTTP 500:\n%s\n%s", r.stdout, r.stderr)
	}
	if len(errLines(r, "iam.serviceAccounts.actAs", "deploy-nightly@")) != 1 {
		t.Errorf("the check on deploy-nightly did not run:\n%s\n%s", r.stdout, r.stderr)
	}
	if len(errLines(r, "Cloud Build")) != 1 {
		t.Errorf("the Cloud Build check did not run:\n%s\n%s", r.stdout, r.stderr)
	}
}

func TestPreviewInvariantsFailClosedOnADenialThatIsNotTheSecretRead(t *testing.T) {
	t.Parallel()
	r := runInvariantsWith(t, []string{"nightly-hubspot-token", "nightly-resend-read-key"}, nlGoodSvcs(),
		map[string]string{"denied": "nightly-resend-read-key\n", "otherdenied": "nightly-hubspot-token\n"})
	if r.code != 1 {
		t.Fatalf("invariants exited %d, want 1:\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	if len(errLines(r, "could not check nightly-hubspot-token")) != 1 {
		t.Errorf("want one ::error:: naming nightly-hubspot-token:\n%s\n%s", r.stdout, r.stderr)
	}
	if strings.Contains(r.stdout, imgHeld) {
		t.Errorf("success line printed:\n%s", r.stdout)
	}
}

func TestPreviewSecretsPRMakesItsMockIssuerToken(t *testing.T) {
	t.Parallel()
	r := runPreview(t, nil, "secrets", "7")
	if r.code != 0 {
		t.Fatalf("secrets exited %d: %s", r.code, r.stderr)
	}
	if tok := r.file(t, "created-pr-7-mock-issuer-token"); !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(tok) {
		t.Errorf("mock issuer token = %q, want 64 lowercase hex and no newline", tok)
	}
	var create bool
	for _, l := range nonBlankLines(r.file(t, "calls")) {
		if strings.HasPrefix(l, "secrets create pr-7-mock-issuer-token ") {
			create = true
			if !strings.Contains(l, "--labels preview-pr=7 ") {
				t.Errorf("create is not labelled preview-pr=7: %s", l)
			}
		}
		if strings.Contains(l, "add-iam-policy-binding") && strings.Contains(l, "pr-7-mock-issuer-token") {
			t.Errorf("the PR token is bound: %s", l)
		}
	}
	if !create {
		t.Errorf("no create call for pr-7-mock-issuer-token:\n%s", r.file(t, "calls"))
	}
}

func TestPreviewSecretsPRKeepsAnExistingMockIssuerToken(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) { writeIn(t, dir, "have", "pr-7-mock-issuer-token\n") }, "secrets", "7")
	if r.code != 0 {
		t.Fatalf("secrets exited %d: %s", r.code, r.stderr)
	}
	calls := r.file(t, "calls")
	if got := strings.Count(calls, "secrets create pr-7-"); got != len(previewList(t, "PR_SECRETS"))-1 {
		t.Errorf("%d creates, want every PR secret but the token:\n%s", got, calls)
	}
	if strings.Contains(calls, "secrets create pr-7-mock-issuer-token ") || strings.Contains(calls, "versions add pr-7-mock-issuer-token ") {
		t.Errorf("the existing token was recreated or re-versioned:\n%s", calls)
	}
}

func TestPreviewOnlyTheGatewayMountsTheMockIssuerToken(t *testing.T) {
	t.Parallel()
	argv := argvBySvc(nameGoldens(t, "7")["deploy-argv"], "pr-7")
	if len(argv) == 0 {
		t.Fatal("no service deployed")
	}
	for svc, line := range argv {
		got := flagValue(line, "--set-secrets")
		if svc == "gateway" {
			if !strings.Contains(got, "GATEWAY_MOCK_ISSUER_TOKEN=pr-7-mock-issuer-token:latest") {
				t.Errorf("gateway --set-secrets = %q, want the mock issuer token", got)
			}
		} else if strings.Contains(got, "mock-issuer-token") {
			t.Errorf("%s mounts the mock issuer token: %q", svc, got)
		}
	}
}

func TestPreviewTeardownDeletesTheTokenSecretsMade(t *testing.T) {
	t.Parallel()
	sec := runPreview(t, nil, "secrets", "7")
	var have strings.Builder
	entries, _ := os.ReadDir(sec.dir)
	for _, e := range entries {
		if n, ok := strings.CutPrefix(e.Name(), "created-"); ok {
			have.WriteString(n + "\n")
		}
	}
	r := runPreview(t, func(dir string) { writeIn(t, dir, "have", have.String()) }, "teardown", "7")
	c := r.file(t, "calls")
	for _, w := range []string{"--filter labels.preview-pr=7 ", "secrets delete pr-7-mock-issuer-token "} {
		if !strings.Contains(c, w) {
			t.Errorf("calls lack %q:\n%s", w, c)
		}
	}
}

func TestPreviewMockTokenReadsThePreviewsSecret(t *testing.T) {
	t.Parallel()
	const value = "shim-output"
	for _, name := range []string{"7", "12", "nightly"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sec := previewSuffix(name) + "-mock-issuer-token"
			r := runPreview(t, func(dir string) { writeIn(t, dir, "access-"+sec, value) }, "mock-token", name)
			calls := nonBlankLines(r.file(t, "calls"))
			if r.code != 0 || r.stdout != value || len(calls) != 1 || !strings.HasPrefix(calls[0], "secrets versions access latest --secret "+sec+" ") {
				t.Fatalf("exit %d, stdout %q, calls %v", r.code, r.stdout, calls)
			}
		})
	}
}

func TestPreviewMockTokenRefusesABadName(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"", "07", "0", "pr-7", "7x", "-7", "7 8", "NIGHTLY", "nightly "} {
		t.Run("name="+bad, func(t *testing.T) {
			t.Parallel()
			r := runPreview(t, nil, "mock-token", bad)
			if r.code == 0 {
				t.Errorf("mock-token %q exited 0", bad)
			}
			if _, err := os.Stat(filepath.Join(r.dir, "calls")); err == nil {
				t.Errorf("mock-token %q called gcloud", bad)
			}
		})
	}
}

// Each preview claims one of two document slots (secret <slot>-claim, label preview-pr=<N>) and mounts that slot's key.

// slots sets the claim state of a runPreview dir: owners maps a slot to the PR whose claim carries the label.
// runPreviewEnv gives PR 7 slot-1 by default; slots removes it first. files are extra shim switches (taken, no-objects, fail-storage-rm, ...).
func slots(t *testing.T, owners, files map[string]string) func(dir string) {
	t.Helper()
	return func(dir string) {
		if err := os.Remove(filepath.Join(dir, "owner-slot-1-claim")); err != nil {
			t.Fatal(err)
		}
		for slot, pr := range owners {
			writeIn(t, dir, "owner-"+slot+"-claim", pr)
		}
		for name, body := range files {
			writeIn(t, dir, name, body)
		}
	}
}

// callLines are the gcloud calls of a run; a script that died before any gcloud call has none.
func callLines(r previewRun) []string {
	b, _ := os.ReadFile(filepath.Join(r.dir, "calls"))
	return nonBlankLines(string(b))
}

// callIdx is the index of the first call holding every part, or -1.
func callIdx(calls []string, parts ...string) int {
	return slices.IndexFunc(calls, func(l string) bool {
		for _, p := range parts {
			if !strings.Contains(l, p) {
				return false
			}
		}
		return true
	})
}

const (
	slot1Docs = "gs://ascomply-preview-docs-slot-1"
	slot2Docs = "gs://ascomply-preview-docs-slot-2"
)

func TestPreviewClaimTakesTheFirstFreeSlot(t *testing.T) {
	t.Parallel()
	r := runPreview(t, slots(t, nil, nil), "claim", "7")
	calls := callLines(r)
	create := callIdx(calls, "secrets create slot-1-claim ", "--labels preview-pr=7")
	ls, rm := callIdx(calls, "storage ls "+slot1Docs+"/**"), callIdx(calls, "storage rm "+slot1Docs+"/**")
	if r.code != 0 || r.stdout != "slot=slot-1\n" {
		t.Errorf("exit %d, stdout %q, stderr %q, want exit 0 and slot=slot-1", r.code, r.stdout, r.stderr)
	}
	if create < 0 || ls < create || rm < ls {
		t.Errorf("want create slot-1-claim, then ls, then rm of %s (indexes %d, %d, %d):\n%s", slot1Docs, create, ls, rm, strings.Join(calls, "\n"))
	}
	if callIdx(calls, "secrets create slot-2-claim") >= 0 {
		t.Errorf("claim 7 also took slot-2:\n%s", strings.Join(calls, "\n"))
	}
}

func TestPreviewClaimSkipsASlotAnotherPreviewHolds(t *testing.T) {
	t.Parallel()
	r := runPreview(t, slots(t, map[string]string{"slot-1": "5"}, map[string]string{"taken": "slot-1-claim\n"}), "claim", "7")
	calls := callLines(r)
	if r.code != 0 || r.stdout != "slot=slot-2\n" {
		t.Errorf("exit %d, stdout %q, stderr %q, want slot=slot-2", r.code, r.stdout, r.stderr)
	}
	if callIdx(calls, "secrets create slot-2-claim ", "--labels preview-pr=7") < 0 || callIdx(calls, "storage rm "+slot2Docs+"/**") < 0 {
		t.Errorf("slot-2 was not claimed and emptied:\n%s", strings.Join(calls, "\n"))
	}
	if i := callIdx(calls, "ascomply-preview-docs-slot-1"); i >= 0 {
		t.Errorf("PR 5's bucket was touched: %s", calls[i])
	}
}

// Race: both describes read NOT_FOUND, then slot-1's create loses to another PR's.
func TestPreviewClaimLosesARaceToTheNextSlot(t *testing.T) {
	t.Parallel()
	r := runPreview(t, slots(t, nil, map[string]string{"taken": "slot-1-claim\n"}), "claim", "7")
	calls := callLines(r)
	first, second := callIdx(calls, "secrets create slot-1-claim "), callIdx(calls, "secrets create slot-2-claim ")
	if r.code != 0 || r.stdout != "slot=slot-2\n" {
		t.Errorf("exit %d, stdout %q, stderr %q, want slot=slot-2", r.code, r.stdout, r.stderr)
	}
	if first < 0 || second < first {
		t.Errorf("want create slot-1-claim, then create slot-2-claim (indexes %d, %d):\n%s", first, second, strings.Join(calls, "\n"))
	}
	if i := callIdx(calls, "ascomply-preview-docs-slot-1"); i >= 0 {
		t.Errorf("the loser touched the winner's bucket: %s", calls[i])
	}
}

// Boundary: the pool is two slots. The nightly's slot is never a PR's, so a third PR waits.
func TestPreviewClaimReportsFullWhenBothSlotsAreHeld(t *testing.T) {
	t.Parallel()
	r := runPreview(t, slots(t, map[string]string{"slot-1": "5", "slot-2": "6"}, map[string]string{"taken": "slot-1-claim\nslot-2-claim\n"}), "claim", "7")
	calls := callLines(r)
	if r.code != 0 || r.stdout != "full=true\n" {
		t.Errorf("exit %d, stdout %q, stderr %q, want exit 0 and full=true", r.code, r.stdout, r.stderr)
	}
	if len(calls) == 0 {
		t.Fatal("claim made no gcloud call: it never looked at the claims")
	}
	for _, c := range calls {
		if strings.HasPrefix(c, "storage ") || strings.Contains(c, "nightly") || strings.HasPrefix(c, "secrets delete") {
			t.Errorf("a full pool still ran %q", c)
		}
	}
}

// A claim labelled 77 or 17 is not PR 7's: the match is the whole label.
func TestPreviewClaimDoesNotMistakeAnotherPRsClaimForItsOwn(t *testing.T) {
	t.Parallel()
	r := runPreview(t, slots(t, map[string]string{"slot-1": "77", "slot-2": "17"}, map[string]string{"taken": "slot-1-claim\nslot-2-claim\n"}), "claim", "7")
	if r.code != 0 || r.stdout != "full=true\n" {
		t.Errorf("exit %d, stdout %q, stderr %q, want full=true", r.code, r.stdout, r.stderr)
	}
	if i := callIdx(callLines(r), "storage "); i >= 0 {
		t.Errorf("PR 7 emptied a bucket it does not hold: %s", callLines(r)[i])
	}
}

func TestPreviewClaimKeepsItsSlotAndEmptiesIt(t *testing.T) {
	t.Parallel()
	r := runPreview(t, slots(t, map[string]string{"slot-2": "7"}, nil), "claim", "7")
	calls := callLines(r)
	if r.code != 0 || r.stdout != "slot=slot-2\n" {
		t.Errorf("exit %d, stdout %q, stderr %q, want slot=slot-2", r.code, r.stdout, r.stderr)
	}
	if callIdx(calls, "storage rm "+slot2Docs+"/**") < 0 {
		t.Errorf("a re-claim did not empty its bucket:\n%s", strings.Join(calls, "\n"))
	}
	if i := callIdx(calls, "secrets create"); i >= 0 {
		t.Errorf("a PR that holds a slot made a claim: %s", calls[i])
	}
}

// Boundary: "matched no objects" is an empty bucket, not an error.
func TestPreviewClaimTreatsNoObjectsAsEmpty(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) { writeIn(t, dir, "no-objects", "") }, "claim", "7")
	calls := callLines(r)
	if r.code != 0 || r.stdout != "slot=slot-1\n" {
		t.Errorf("exit %d, stdout %q, stderr %q, want exit 0 and slot=slot-1", r.code, r.stdout, r.stderr)
	}
	if callIdx(calls, "storage ls "+slot1Docs+"/**") < 0 {
		t.Errorf("claim never listed the bucket:\n%s", strings.Join(calls, "\n"))
	}
	if i := callIdx(calls, "storage rm"); i >= 0 {
		t.Errorf("an empty bucket was emptied: %s", calls[i])
	}
}

func TestPreviewClaimDiesWhenEmptyingFails(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) { writeIn(t, dir, "fail-storage-rm", "") }, "claim", "7")
	if callIdx(callLines(r), "storage rm "+slot1Docs+"/**") < 0 {
		t.Errorf("claim never tried to empty the bucket:\n%s", strings.Join(callLines(r), "\n"))
	}
	if r.code == 0 || !strings.Contains(r.stderr, slot1Docs) || strings.Contains(r.stdout, "slot=") {
		t.Errorf("exit %d, stdout %q, stderr %q, want non-zero, %s named, no slot=", r.code, r.stdout, r.stderr, slot1Docs)
	}
}

func TestPreviewClaimDiesWhenTheListingFails(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) { writeIn(t, dir, "fail-storage-ls", "") }, "claim", "7")
	calls := callLines(r)
	if callIdx(calls, "storage ls "+slot1Docs+"/**") < 0 {
		t.Errorf("claim never listed the bucket:\n%s", strings.Join(calls, "\n"))
	}
	if i := callIdx(calls, "storage rm"); i >= 0 {
		t.Errorf("an unreadable bucket was emptied blind: %s", calls[i])
	}
	if r.code == 0 || strings.Contains(r.stdout, "slot=") {
		t.Errorf("exit %d, stdout %q, want non-zero and no slot=", r.code, r.stdout)
	}
}

func TestPreviewClaimDiesOnACreateError(t *testing.T) {
	t.Parallel()
	r := runPreview(t, slots(t, nil, map[string]string{"fail-secrets-create": ""}), "claim", "7")
	calls := callLines(r)
	if n := strings.Count(strings.Join(calls, "\n"), "secrets create"); n != 1 {
		t.Errorf("%d create calls, want exactly 1: an error that is not ALREADY_EXISTS is no reason to try the next slot:\n%s", n, strings.Join(calls, "\n"))
	}
	if r.code == 0 || !strings.Contains(r.stderr, "slot-1-claim") || strings.Contains(r.stdout, "slot=") || strings.Contains(r.stdout, "full=") {
		t.Errorf("exit %d, stdout %q, stderr %q, want non-zero naming slot-1-claim, with no verdict", r.code, r.stdout, r.stderr)
	}
}

func TestPreviewClaimRefusesTheNightly(t *testing.T) {
	t.Parallel()
	r := runPreview(t, nil, "claim", "nightly")
	if r.code == 0 || !strings.Contains(r.stderr, "nightly") || strings.Contains(r.stdout, "slot=") {
		t.Errorf("exit %d, stdout %q, stderr %q, want a refusal that names the nightly", r.code, r.stdout, r.stderr)
	}
	if i := callIdx(callLines(r), "secrets"); i >= 0 {
		t.Errorf("claim nightly reached Secret Manager: %s", callLines(r)[i])
	}
}

func TestPreviewUsageListsClaim(t *testing.T) {
	t.Parallel()
	if r := runPreview(t, nil); r.code == 0 || !strings.Contains(r.stderr, "|claim|") {
		t.Errorf("exit %d, usage %q lacks claim", r.code, r.stderr)
	}
}

func TestPreviewBucketNeedsAClaim(t *testing.T) {
	t.Parallel()
	r := runPreview(t, slots(t, nil, nil), "bucket", "7")
	if r.code == 0 || !strings.Contains(r.stderr, "holds no preview slot") {
		t.Errorf("exit %d, stderr %q, want non-zero with %q", r.code, r.stderr, "holds no preview slot")
	}
	if i := callIdx(callLines(r), "buckets create"); i >= 0 {
		t.Errorf("a PR without a slot got a bucket: %s", callLines(r)[i])
	}
}

func TestPreviewBucketChecksTheSlotsBucketAndMakesNone(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) { writeIn(t, dir, "exists", slot1Docs+"\n") }, "bucket", "7")
	calls := callLines(r)
	if r.code != 0 || callIdx(calls, "storage buckets describe "+slot1Docs) < 0 {
		t.Errorf("exit %d, stderr %q, want a describe of %s:\n%s", r.code, r.stderr, slot1Docs, strings.Join(calls, "\n"))
	}
	for _, bad := range []string{"buckets create", "buckets update", "--update-labels"} {
		if i := callIdx(calls, bad); i >= 0 {
			t.Errorf("bucket made or relabelled a bucket: %s", calls[i])
		}
	}
}

func TestPreviewBucketDiesWhenTheSlotBucketIsMissing(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) { writeIn(t, dir, "exists", "none\n") }, "bucket", "7")
	if r.code == 0 || !strings.Contains(r.stderr, "ascomply-preview-docs-slot-1") || !strings.Contains(r.stderr, "preview.tf") {
		t.Errorf("exit %d, stderr %q, want non-zero naming the slot bucket and invoice-os-infra preview.tf", r.code, r.stderr)
	}
	if i := callIdx(callLines(r), "buckets create"); i >= 0 {
		t.Errorf("a missing slot bucket was created: %s", callLines(r)[i])
	}
}

func TestPreviewBucketNightlyUsesItsFixedSlot(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) { writeIn(t, dir, "exists", "gs://ascomply-preview-docs-nightly\n") }, "bucket", "nightly")
	calls := callLines(r)
	if r.code != 0 || callIdx(calls, "storage buckets describe gs://ascomply-preview-docs-nightly") < 0 {
		t.Errorf("exit %d, stderr %q, want a describe of the nightly slot bucket:\n%s", r.code, r.stderr, strings.Join(calls, "\n"))
	}
	for _, bad := range []string{"secrets describe", "buckets create", "--update-labels"} {
		if i := callIdx(calls, bad); i >= 0 {
			t.Errorf("bucket nightly ran %q: %s", bad, calls[i])
		}
	}
}

func TestPreviewDeployDiesWithoutASlot(t *testing.T) {
	t.Parallel()
	r := runPreview(t, slots(t, nil, nil), "deploy", "7", "abc123", "invoice")
	if r.code == 0 || !strings.Contains(r.stderr, "holds no preview slot") {
		t.Errorf("exit %d, stderr %q, want non-zero with %q", r.code, r.stderr, "holds no preview slot")
	}
	if i := callIdx(callLines(r), "run deploy"); i >= 0 {
		t.Errorf("a PR without a slot deployed: %s", callLines(r)[i])
	}
}

func TestPreviewMigrateDiesWithoutASlot(t *testing.T) {
	t.Parallel()
	r := runPreview(t, slots(t, nil, nil), "migrate", "7", "abc123")
	if r.code == 0 || !strings.Contains(r.stderr, "holds no preview slot") {
		t.Errorf("exit %d, stderr %q, want non-zero with %q", r.code, r.stderr, "holds no preview slot")
	}
	for _, bad := range []string{"run jobs deploy", "run jobs execute"} {
		if i := callIdx(callLines(r), bad); i >= 0 {
			t.Errorf("a PR without a slot ran %q: %s", bad, callLines(r)[i])
		}
	}
}

func TestPreviewServicesMountTheirSlotsKey(t *testing.T) {
	t.Parallel()
	svcs := []string{"invoice", "submission", "worker"}
	for _, c := range []struct {
		name   string
		owners map[string]string
		slot   string
	}{
		{"the default slot-1", map[string]string{"slot-1": "7"}, "slot-1"},
		{"slot-2, with slot-1 held by PR 5", map[string]string{"slot-1": "5", "slot-2": "7"}, "slot-2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			setup := slots(t, c.owners, nil)
			dep := runPreview(t, setup, append([]string{"deploy", "7", "abc123"}, svcs...)...)
			mig := runPreview(t, setup, "migrate", "7", "abc123")
			if dep.code != 0 || mig.code != 0 {
				t.Fatalf("deploy exit %d (%s), migrate exit %d (%s)", dep.code, dep.stderr, mig.code, mig.stderr)
			}
			wantKeys := []string{"DOCUMENT_ACCESS_KEY_ID=" + c.slot + "-document-access-key-id:latest", "DOCUMENT_SECRET_ACCESS_KEY=" + c.slot + "-document-secret-key:latest"}
			wantBucket := "DOCUMENT_BUCKET: ascomply-preview-docs-" + c.slot + "\n"
			argv := deployCalls(t, dep, "7")
			mounts := map[string]string{"migrate": callLines(mig)[callIdx(callLines(mig), "run jobs deploy migrate-pr-7 ")]}
			envs := map[string]string{"migrate": mig.file(t, "env-job-migrate-pr-7")}
			for _, svc := range svcs {
				mounts[svc], envs[svc] = argv[svc], dep.file(t, "env-"+svc+"-pr-7")
			}
			if len(mounts) != len(svcs)+1 {
				t.Fatalf("%d mounts seen, want %d", len(mounts), len(svcs)+1)
			}
			for name, line := range mounts {
				set := flagValue(line, "--set-secrets")
				for _, w := range wantKeys {
					if !strings.Contains(","+set+",", ","+w+",") {
						t.Errorf("%s --set-secrets lacks %s: %s", name, w, set)
					}
				}
				if strings.Contains(set, "=document-access-key-id:") || strings.Contains(set, "=document-secret-key:") {
					t.Errorf("%s still mounts the shared key: %s", name, set)
				}
				if !strings.Contains(envs[name], wantBucket) {
					t.Errorf("%s env lacks %q:\n%s", name, wantBucket, envs[name])
				}
			}
		})
	}
}

// Boundary: the nightly takes no claim, so it reads no secret to find its slot.
func TestPreviewNightlyMountsItsOwnSlotKey(t *testing.T) {
	t.Parallel()
	env := []string{"NIGHTLY_HUBSPOT_TOKEN_VERSION=7"}
	dep := runPreviewEnv(t, env, nil, "deploy", "nightly", "abc123", "invoice")
	mig := runPreviewEnv(t, env, nil, "migrate", "nightly", "abc123")
	if dep.code != 0 || mig.code != 0 {
		t.Fatalf("deploy exit %d (%s), migrate exit %d (%s)", dep.code, dep.stderr, mig.code, mig.stderr)
	}
	svcs := deployCalls(t, dep, "nightly") // nightly services carry no -pr- suffix: keys are invoice-nightly, ...
	if _, ok := svcs["invoice-nightly"]; !ok || len(svcs) == 0 {
		t.Fatalf("no invoice-nightly deploy call: %v", svcs)
	}
	for name, line := range map[string]string{"invoice": svcs["invoice-nightly"], "migrate": callLines(mig)[callIdx(callLines(mig), "run jobs deploy migrate-nightly ")]} {
		set := flagValue(line, "--set-secrets")
		for _, w := range []string{"DOCUMENT_ACCESS_KEY_ID=nightly-document-access-key-id:latest", "DOCUMENT_SECRET_ACCESS_KEY=nightly-document-secret-key:latest"} {
			if !strings.Contains(","+set+",", ","+w+",") {
				t.Errorf("%s --set-secrets lacks %s: %s", name, w, set)
			}
		}
	}
	for name, e := range map[string]string{"invoice": dep.file(t, "env-invoice-nightly"), "migrate": mig.file(t, "env-job-migrate-nightly")} {
		if !strings.Contains(e, "DOCUMENT_BUCKET: ascomply-preview-docs-nightly\n") {
			t.Errorf("%s env lacks the nightly slot bucket:\n%s", name, e)
		}
	}
	for _, r := range []previewRun{dep, mig} {
		if i := callIdx(callLines(r), "secrets describe"); i >= 0 {
			t.Errorf("the nightly looked up a claim: %s", callLines(r)[i])
		}
	}
}

func TestPreviewTeardownEmptiesTheSlotThenReleasesIt(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) { writeIn(t, dir, "have-7", "pr-7-gateway-token\nslot-1-claim\n") }, "teardown", "7")
	calls := callLines(r)
	rm, tok := callIdx(calls, "storage rm "+slot1Docs+"/**"), callIdx(calls, "secrets delete pr-7-gateway-token")
	claim := callIdx(calls, "secrets delete slot-1-claim ")
	if r.code != 0 || r.stdout != "PR 7: preview deleted\n" {
		t.Errorf("exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
	if rm < 0 || tok < 0 || claim < 0 || rm > claim || tok > claim {
		t.Errorf("want the slot emptied and every other secret deleted before slot-1-claim (indexes rm %d, token %d, claim %d):\n%s", rm, tok, claim, strings.Join(calls, "\n"))
	}
	if n := strings.Count(strings.Join(calls, "\n"), "secrets delete slot-1-claim "); n != 1 {
		t.Errorf("slot-1-claim deleted %d times, want once (the label loop skips it)", n)
	}
	for _, bad := range []string{"buckets delete", "rm --recursive gs://ascomply-preview-docs-", "slot-2"} {
		if i := callIdx(calls, bad); i >= 0 {
			t.Errorf("teardown ran %q: %s", bad, calls[i])
		}
	}
}

func TestPreviewTeardownKeepsTheClaimWhenEmptyingFails(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) {
		writeIn(t, dir, "have-7", "pr-7-gateway-token\nslot-1-claim\n")
		writeIn(t, dir, "fail-storage-rm", "")
		writeIn(t, dir, "exists", "gateway-pr-7\nmigrate-pr-7\n") // no legacy bucket: its rm would fail too
	}, "teardown", "7")
	calls := callLines(r)
	if callIdx(calls, "storage rm "+slot1Docs+"/**") < 0 {
		t.Errorf("teardown never tried to empty the slot:\n%s", strings.Join(calls, "\n"))
	}
	if r.code == 0 || !strings.Contains(r.stderr, "left something behind") {
		t.Errorf("exit %d, stderr %q, want non-zero with %q", r.code, r.stderr, "left something behind")
	}
	if callIdx(calls, "secrets delete pr-7-gateway-token") < 0 {
		t.Errorf("a failed empty stopped the other secrets going:\n%s", strings.Join(calls, "\n"))
	}
	if i := callIdx(calls, "secrets delete slot-1-claim"); i >= 0 {
		t.Errorf("the claim was released over a bucket that still holds objects: %s", calls[i])
	}
}

// A service or job that stays behind still mounts the slot's key and bucket: the sweeper retries, so the claim stays.
func TestPreviewTeardownKeepsTheClaimWhenAServiceOrJobDeleteFails(t *testing.T) {
	t.Parallel()
	for _, fail := range []string{"fail-run-services-delete", "fail-run-jobs-delete"} {
		t.Run(fail, func(t *testing.T) {
			t.Parallel()
			r := runPreview(t, func(dir string) {
				writeIn(t, dir, "have-7", "pr-7-gateway-token\nslot-1-claim\n")
				writeIn(t, dir, fail, "")
				writeIn(t, dir, "exists", "gateway-pr-7\nmigrate-pr-7\n")
			}, "teardown", "7")
			calls := callLines(r)
			if r.code == 0 || !strings.Contains(r.stderr, "left something behind") {
				t.Errorf("exit %d, stderr %q, want non-zero with %q", r.code, r.stderr, "left something behind")
			}
			if i := callIdx(calls, "secrets delete slot-1-claim"); i >= 0 {
				t.Errorf("the claim was released over a leftover service or job: %s", calls[i])
			}
			if callIdx(calls, "storage rm "+slot1Docs+"/**") < 0 || callIdx(calls, "secrets delete pr-7-gateway-token") < 0 {
				t.Errorf("a failed delete stopped the rest of the teardown:\n%s", strings.Join(calls, "\n"))
			}
		})
	}
}

// A describe that fails for any reason but NOT_FOUND may hide a running service: the claim stays.
func TestPreviewTeardownKeepsTheClaimWhenADescribeFails(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"gateway-pr-7", "migrate-pr-7"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := runPreview(t, func(dir string) {
				writeIn(t, dir, "have-7", "pr-7-gateway-token\nslot-1-claim\n")
				writeIn(t, dir, "describe-run-error", name+"\n")
				writeIn(t, dir, "exists", "gateway-pr-7\nmigrate-pr-7\n")
			}, "teardown", "7")
			calls := callLines(r)
			if r.code == 0 || !strings.Contains(r.stderr, "left something behind") {
				t.Errorf("exit %d, stderr %q, want non-zero with %q", r.code, r.stderr, "left something behind")
			}
			if i := callIdx(calls, "secrets delete slot-1-claim"); i >= 0 {
				t.Errorf("the claim was released over an unreadable service or job: %s", calls[i])
			}
			if callIdx(calls, "storage rm "+slot1Docs+"/**") < 0 || callIdx(calls, "secrets delete pr-7-gateway-token") < 0 {
				t.Errorf("a failed describe stopped the rest of the teardown:\n%s", strings.Join(calls, "\n"))
			}
		})
	}
}

// An empty slot would name ascomply-preview-docs- and -document-access-key-id.
func TestPreviewServiceConfigRefusesAnEmptySlot(t *testing.T) {
	t.Parallel()
	const prog = `source "$0" budget >/dev/null; set +e; unset SLOT
(env_file 7 invoice "$1"); echo "env=$?"
(secrets_of 7 invoice); echo "secrets=$?"
SLOT=slot-1; (secrets_of 7 invoice); echo "claimed=$?"`
	dir := t.TempDir()
	cmd := exec.Command("bash", "-c", prog, filepath.Join(repoRoot(t), "scripts/ci/preview.sh"), filepath.Join(dir, "env.yaml"))
	stdout, stderr, err := runToFiles(t, cmd, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "claimed=0") || strings.Contains(stdout, "env=0") || strings.Contains(stdout, "secrets=0") {
		t.Errorf("want both calls to fail without a slot and to pass with one: stdout %q, stderr %q", stdout, stderr)
	}
	if strings.Contains(stdout, "ascomply-preview-docs-\n") || strings.Contains(stdout, "=-document-") {
		t.Errorf("an empty slot reached the output: %q", stdout)
	}
}

// PR 7 holds slot-2 while PR 5 holds slot-1: the teardown leaves PR 5's slot alone.
func TestPreviewTeardownReleasesOnlyItsOwnSlot(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) {
		slots(t, map[string]string{"slot-1": "5", "slot-2": "7"}, nil)(dir)
		writeIn(t, dir, "have-7", "pr-7-gateway-token\nslot-2-claim\n")
	}, "teardown", "7")
	calls := callLines(r)
	if r.code != 0 || callIdx(calls, "storage rm "+slot2Docs+"/**") < 0 || callIdx(calls, "secrets delete slot-2-claim ") < 0 {
		t.Errorf("exit %d, stderr %q, want slot-2 emptied and its claim released:\n%s", r.code, r.stderr, strings.Join(calls, "\n"))
	}
	for _, bad := range []string{"ascomply-preview-docs-slot-1", "secrets delete slot-1"} {
		if i := callIdx(calls, bad); i >= 0 {
			t.Errorf("PR 7's teardown touched PR 5's slot: %s", calls[i])
		}
	}
}

// Boundary: a PR that never claimed leaves every slot alone.
func TestPreviewTeardownWithoutAClaimTouchesNoSlot(t *testing.T) {
	t.Parallel()
	r := runPreview(t, slots(t, nil, nil), "teardown", "7")
	calls := callLines(r)
	if r.code != 0 {
		t.Errorf("exit %d, stderr %q", r.code, r.stderr)
	}
	if !strings.Contains(strings.Join(calls, "\n"), "slot-1-claim") {
		t.Errorf("teardown never looked for the PR's claim:\n%s", strings.Join(calls, "\n"))
	}
	for _, bad := range []string{"secrets delete slot-", "secrets delete -claim", "ascomply-preview-docs-"} {
		if i := callIdx(calls, bad); i >= 0 {
			t.Errorf("teardown ran %q: %s", bad, calls[i])
		}
	}
}

// Legacy: a preview built before this change still has its own bucket ([S8]).
func TestPreviewTeardownStillDeletesALegacyBucket(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) { writeIn(t, dir, "exists", "gs://ascomply-preview-pr-7\n") }, "teardown", "7")
	calls := callLines(r)
	if r.code != 0 || callIdx(calls, "storage rm --recursive gs://ascomply-preview-pr-7") < 0 {
		t.Errorf("exit %d, stderr %q, want the legacy bucket deleted:\n%s", r.code, r.stderr, strings.Join(calls, "\n"))
	}
	if callIdx(calls, "storage rm "+slot1Docs+"/**") < 0 {
		t.Errorf("the slot was not emptied beside the legacy bucket:\n%s", strings.Join(calls, "\n"))
	}
}

// The sweeper finds a closed PR's slot through its label: a PR whose only resource is the claim is still listed, so a failed empty is retried.
func TestPreviewPrsListsAPRThatHoldsOnlyItsClaim(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) { writeIn(t, dir, "list-secrets", "7 2026-11-17T00:00:00\n") }, "prs")
	if r.code != 0 || !strings.HasPrefix(r.stdout, "7 ") {
		t.Errorf("exit %d, stdout %q, want PR 7 listed", r.code, r.stdout)
	}
}

// A describe error is not "no claim": a holder must not take a second slot, nor lose its claim over a full bucket.
func TestPreviewClaimDiesWhenAClaimCannotBeRead(t *testing.T) {
	t.Parallel()
	r := runPreview(t, slots(t, map[string]string{"slot-2": "7"}, map[string]string{"describe-error": "slot-1-claim\n"}), "claim", "7")
	calls := strings.Join(callLines(r), "\n")
	if r.code == 0 || strings.Contains(r.stdout, "slot=") || strings.Contains(r.stdout, "full=") || !strings.Contains(r.stderr, "slot-1-claim") {
		t.Errorf("exit %d, stdout %q, stderr %q, want non-zero naming slot-1-claim, with no verdict", r.code, r.stdout, r.stderr)
	}
	if strings.Contains(calls, "secrets create") || strings.Contains(calls, "storage rm") {
		t.Errorf("claim acted on an unreadable claim:\n%s", calls)
	}
}

func TestPreviewTeardownKeepsTheClaimWhenItCannotBeRead(t *testing.T) {
	t.Parallel()
	r := runPreview(t, func(dir string) {
		writeIn(t, dir, "have-7", "pr-7-gateway-token\nslot-1-claim\n")
		writeIn(t, dir, "describe-error", "slot-1-claim\n")
		writeIn(t, dir, "exists", "gateway-pr-7\nmigrate-pr-7\n")
	}, "teardown", "7")
	calls := strings.Join(callLines(r), "\n")
	if r.code == 0 || !strings.Contains(r.stderr, "left something behind") {
		t.Errorf("exit %d, stderr %q, want non-zero with %q", r.code, r.stderr, "left something behind")
	}
	if strings.Contains(calls, "secrets delete slot-1-claim") {
		t.Errorf("the claim was released over a read error:\n%s", calls)
	}
	if !strings.Contains(calls, "secrets delete pr-7-gateway-token") {
		t.Errorf("a read error stopped the other secrets going:\n%s", calls)
	}
}
