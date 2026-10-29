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
"run services list") cat "$D/list-services" 2>/dev/null || : ;;
"run jobs list") cat "$D/list-jobs" 2>/dev/null || : ;;
"storage buckets list") cat "$D/list-buckets" 2>/dev/null || : ;;
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

	dep := runPreview(t, nil, append([]string{"deploy", name, "abc123"}, prAll(t)...)...)
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
		if want := asNightly(prGolden(t, "env-"+svc)); got["env-"+svc] != want {
			t.Errorf("env-%s:\n%s\nwant\n%s", svc, got["env-"+svc], want)
		}
	}
	if want := asNightly(prGolden(t, "env-migrate")); got["env-migrate"] != want {
		t.Errorf("migrate env:\n%s\nwant\n%s", got["env-migrate"], want)
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
		if got := flagValue(ni[svc], "--set-secrets"); got != want {
			t.Errorf("%s --set-secrets = %q, want %q", svc, got, want)
		}
		if !strings.Contains(ni[svc], "--labels preview-pr=nightly ") {
			t.Errorf("%s is not labelled preview-pr=nightly: %s", svc, ni[svc])
		}
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
		writeIn(t, dir, "have-nightly", "nightly-gateway-token\nnightly-s2s-token\n")
		writeIn(t, dir, "have-7", "pr-7-gateway-token\n")
	}, "teardown", "nightly")
	if r.code != 0 || r.stdout != "PR nightly: preview deleted\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
	var deletes []string
	for _, l := range nonBlankLines(r.file(t, "calls")) {
		if strings.Contains(l, " delete ") || strings.Contains(l, "storage rm ") {
			deletes = append(deletes, strings.Fields(l)[2]+" "+strings.Fields(l)[3])
		}
	}
	want := []string{"services gateway-nightly", "jobs migrate-nightly", "rm --recursive", "delete nightly-gateway-token", "delete nightly-s2s-token"}
	if got := strings.Join(deletes, "|"); len(deletes) != len(want) || strings.Contains(got, "pr-7") {
		t.Errorf("deletes = %v, want %v", deletes, want)
	}
	c := r.file(t, "calls")
	for _, w := range []string{"gs://ascomply-preview-nightly", "--filter labels.preview-pr=nightly ", "PR_DB=pr_0,ACTION=drop"} {
		if !strings.Contains(c, w) {
			t.Errorf("calls lack %q", w)
		}
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
