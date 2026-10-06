// railway_env_dsn_test.go pins the WIRING contract of the not-yet-implemented
// `scripts/ci/railway-env.sh assert-db-dsns` subcommand (M4-22-FU-02,
// task-178). dsn_check_test.go proves the checker is correct; this file
// proves the checker is actually REACHED, actually fails the job, and does
// not leak on the way. The batched-read tests at the end drive the live path
// against newAuthShim.
//
// WHY A GO TEST FOR A SHELL SCRIPT. Precedent: cmd/gateway/main_test.go
// :130-181 already asserts on repo source text from a Go test, for the same
// reason -- `go test ./...` is the one gate that always runs, so a guard
// living anywhere else is a guard that can be skipped.
//
// TOKEN-FREE AND NETWORK-FREE, BY CONSTRUCTION. `--self-test` must short-circuit BEFORE require_env (the
// `--self-test` branch at the top of cmd_assert_db_dsns in
// scripts/ci/railway-env.sh). T2-4
// asserts the short-circuit ordering directly by unsetting the token. No test
// in this file skips: a test that silently skips in CI is a decorative test.
//
// ASSUMED CONTRACT -- FLAGGED FOR THE EXECUTOR. The plan fixes that
// `--self-test` needs no token and no network, but does not say where its map
// comes from, and T2-1/T2-3 need to drive a BAD map and a GOOD map
// separately. These tests therefore assume the map arrives on STDIN, the same
// channel `dsn-check` uses and for the same reason: argv is visible in `ps`
// and this map carries live credentials. If the executor picks a different
// input channel, this file must be updated deliberately -- not silently
// loosened.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// repoRoot resolves the repository this test is running in. Uses git rather
// than a relative path so the test is correct under a worktree checkout --
// which is where this story is being developed.
func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func railwayEnvScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(repoRoot(t), "scripts", "ci", "railway-env.sh")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("railway-env.sh not found at %s: %v", path, err)
	}
	return path
}

// runSelfTest execs `railway-env.sh assert-db-dsns --self-test` with m on
// stdin. extraEnv entries are appended to the inherited environment;
// unsetVars are removed from it. Returns combined stdout, stderr, exit code.
func runSelfTest(t *testing.T, m dsnMap, extraEnv []string, unsetVars ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshalling fixture map: %v", err)
	}

	cmd := exec.Command("bash", railwayEnvScript(t), "assert-db-dsns", "--self-test")
	cmd.Stdin = strings.NewReader(string(raw))
	cmd.Dir = repoRoot(t)

	env := os.Environ()
	if len(unsetVars) > 0 {
		var filtered []string
		for _, kv := range env {
			drop := false
			for _, name := range unsetVars {
				if strings.HasPrefix(kv, name+"=") {
					drop = true
					break
				}
			}
			if !drop {
				filtered = append(filtered, kv)
			}
		}
		env = filtered
	}
	cmd.Env = append(env, extraEnv...)

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("failed to run railway-env.sh: %v", err)
		}
		exitCode = exitErr.ExitCode()
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// badSelfTestMap is the shared bad fixture: the verbatim M4-22 incident DSN
// in the slot it actually occupied.
func badSelfTestMap() dsnMap {
	m := healthyMap()
	m["gateway"]["DATABASE_MIGRATION_URL"] = incidentDSN
	return m
}

// T2-1. The self-test must actually FAIL the job on a bad map, and must
// surface the offender through GitHub Actions' `::error::` annotation channel
// naming both the service and the variable. A self-test that always exits 0
// is the exact decorative-guard shape this whole story exists to prevent: it
// is green, it is wired into CI, and it cannot fail.
//
// Asserts exit != 0 only, per the two-way exit contract (see
// dsn_check_test.go's package comment).
//
// KILLS: M-selftest0.
func TestAssertDBDSNsSelfTestFailsOnBadMap(t *testing.T) {
	stdout, stderr, code := runSelfTest(t, badSelfTestMap(), nil)

	if code == 0 {
		t.Errorf("exit code = 0, want non-zero: the self-test fed a DSN with an empty password and must fail; stdout = %q, stderr = %q", stdout, stderr)
	}
	if !strings.Contains(stdout, "::error::") {
		t.Errorf("stdout carries no ::error:: annotation -- without it the failure is invisible in the Actions UI; stdout = %q", stdout)
	}
	if !strings.Contains(stdout, "gateway") {
		t.Errorf("stdout does not name the offending service %q; stdout = %q", "gateway", stdout)
	}
	if !strings.Contains(stdout, "DATABASE_MIGRATION_URL") {
		t.Errorf("stdout does not name the offending variable %q; stdout = %q", "DATABASE_MIGRATION_URL", stdout)
	}
}

// T2-2. Credential hygiene at the WIRING layer, not just inside the checker.
// The shell is where a stray `set -x`, an `echo "$map"`, or a `::error::` that
// interpolates the whole payload would leak -- dsn_check_test.go's T1-9 cannot
// see any of those.
//
// KILLS: M-mask / credential echo at the wiring layer.
func TestAssertDBDSNsSelfTestNeverEchoesACredential(t *testing.T) {
	stdout, stderr, code := runSelfTest(t, badSelfTestMap(), nil)

	if code == 0 {
		t.Errorf("exit code = 0 -- this run must FAIL for the leak assertions below to be meaningful rather than vacuous")
	}
	// NON-VACUITY PRECONDITION -- see the same guard in dsn_check_test.go's
	// TestDSNCheckNeverEchoesACredential. A script that never reaches the
	// check leaks nothing, so the leak assertions must be gated on the check
	// having actually reported the offender.
	if !strings.Contains(stdout, "gateway") || !strings.Contains(stdout, "DATABASE_MIGRATION_URL") {
		t.Errorf("the self-test did not report the offender -- 'it leaked no credential' is vacuous until it produces a real report; stdout = %q", stdout)
	}
	if strings.Contains(stdout, sentinelPW) {
		t.Errorf("stdout leaked the password sentinel %q into the CI log; stdout = %q", sentinelPW, stdout)
	}
	if strings.Contains(stderr, sentinelPW) {
		t.Errorf("stderr leaked the password sentinel %q into the CI log; stderr = %q", sentinelPW, stderr)
	}
}

// T2-3. The inverted-self-test guard. A self-test that fails on a healthy map
// is worse than none: it blocks every deploy until someone disables it, and
// then nothing is checked at all.
//
// KILLS: inverted self-test.
func TestAssertDBDSNsSelfTestPassesOnGoodMap(t *testing.T) {
	stdout, stderr, code := runSelfTest(t, healthyMap(), nil)

	if code != 0 {
		t.Errorf("exit code = %d, want 0: the fleet map is entirely healthy; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if strings.Contains(stdout, "::error::") {
		t.Errorf("stdout emits an ::error:: annotation for a healthy map; stdout = %q", stdout)
	}
}

// T2-4. Pins the ORDERING that makes the self-test runnable at all: the
// `--self-test` short-circuit must come BEFORE require_env inside
// cmd_assert_db_dsns. With RAILWAY_API_TOKEN unset, a self-test placed
// after require_env exits 1 with "RAILWAY_API_TOKEN is not set" --
// which is a FAILING self-test that never ran the check, and which on a fork
// PR (no secrets, by design) would fail every build.
//
// RAILWAY_PROJECT_ID is unset too: require_env checks both, so leaving one set
// would let the test pass against a half-moved short-circuit.
//
// KILLS: require_env moved ahead of the short-circuit.
func TestAssertDBDSNsSelfTestNeedsNoToken(t *testing.T) {
	stdout, stderr, code := runSelfTest(t, healthyMap(), nil, "RAILWAY_API_TOKEN", "RAILWAY_PROJECT_ID")

	if code != 0 {
		t.Errorf("exit code = %d, want 0 with RAILWAY_API_TOKEN and RAILWAY_PROJECT_ID unset: --self-test must short-circuit before require_env, so it needs no token and no network; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if strings.Contains(stdout, "RAILWAY_API_TOKEN") || strings.Contains(stderr, "RAILWAY_API_TOKEN") {
		t.Errorf("the self-test reached require_env -- it must short-circuit before it; stdout = %q, stderr = %q", stdout, stderr)
	}
}

// T2-DOC. The two DOCUMENT_* self-test fixtures, driven through the same
// `assert-db-dsns --self-test` path as every other fixture here. The Go tests
// in dsn_test.go prove the classification; this proves run_dsn_check and
// CheckDSNs handle the new rows and exit the right way.
//
// WHAT THIS DOES *NOT* COVER. `--self-test` cats stdin straight into
// run_dsn_check and returns before cmd_assert_db_dsns builds $prefixes or
// applies the rendered-variable filter, so the DOCUMENT prefix never reaches
// that filter here. TestEverySeverityTableRowSurvivesTheShellPrefixFilter is
// the ONLY guard on the filter, and it is static -- a reader must not take
// this test for end-to-end coverage of the prefix path.
//
// The clean half is not merely a false-positive guard: the five rendered values
// are opaque tokens, not URLs, so a KindOpaque that forgot to skip the URL
// checks fails here with five DefectNoPassword offenders.
func TestAssertDBDSNsSelfTestCoversDocumentVars(t *testing.T) {
	t.Run("document-vars-clean", func(t *testing.T) {
		requireDocumentRows(t, "invoice")

		stdout, stderr, code := runSelfTest(t, documentMap(), nil)
		if code != 0 {
			t.Errorf("exit code = %d, want 0: all five DOCUMENT_* variables are rendered and non-empty; stdout = %q, stderr = %q", code, stdout, stderr)
		}
		if strings.Contains(stdout, "::error::") {
			t.Errorf("stdout emits an ::error:: annotation for a fully rendered map; stdout = %q", stdout)
		}
	})

	t.Run("document-vars-unrendered", func(t *testing.T) {
		ref := unrenderedRef("DOCUMENT_BUCKET")
		m := documentMap()
		m["invoice"]["DOCUMENT_BUCKET"] = ref

		stdout, stderr, code := runSelfTest(t, m, nil)
		if code == 0 {
			t.Errorf("exit code = 0, want non-zero: a DOCUMENT_BUCKET that forked as an unrendered reference boots the invoice service against a bucket that does not exist; stdout = %q, stderr = %q", stdout, stderr)
		}
		if !strings.Contains(stdout, "::error::") {
			t.Errorf("stdout carries no ::error:: annotation -- the failure is invisible in the Actions UI; stdout = %q", stdout)
		}
		if !strings.Contains(stdout, "invoice") || !strings.Contains(stdout, "DOCUMENT_BUCKET") {
			t.Errorf("stdout does not name the offending service and variable; stdout = %q", stdout)
		}
		if strings.Contains(stdout, ref) || strings.Contains(stderr, ref) {
			t.Errorf("the report echoed the offending value %q; stdout = %q, stderr = %q", ref, stdout, stderr)
		}
		for _, secret := range []string{sentinelEndpoint, sentinelRegion, sentinelKeyID, sentinelSecret} {
			if strings.Contains(stdout, secret) || strings.Contains(stderr, secret) {
				t.Errorf("the report echoed %q, the value of a non-offending DOCUMENT_* variable; stdout = %q", secret, stdout)
			}
		}
		assertNoSentinel(t, stdout, stderr)
	})
}

// T2-5. The severity table must be derived from service NAMES, never from
// hardcoded Railway service UUIDs. A UUID is environment-scoped: hardcoding
// the `development` fleet's ids makes the check silently inert in every
// per-PR environment, where the same services carry different ids -- a guard
// that is green precisely where it is needed least.
//
// Precedent for grepping repo source from a Go test:
// cmd/gateway/main_test.go:149,181.
//
// HONESTY NOTE -- THIS TEST IS GREEN TODAY. railway-env.sh currently contains
// ZERO UUIDs (measured), so unlike every other test in this file it does not
// start RED. It is a REGRESSION GUARD, not a specification of unbuilt
// behaviour: it fails the moment the executor reaches for a UUID while
// implementing cmd_assert_db_dsns. Recording this rather than dressing it up
// as a RED spec.
//
// KILLS: M-hardcode.
func TestRailwayEnvScriptHardcodesNoServiceUUIDs(t *testing.T) {
	path := railwayEnvScript(t)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	uuidPattern := regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	hits := uuidPattern.FindAllString(string(content), -1)
	if len(hits) > 0 {
		t.Errorf("railway-env.sh hardcodes %d UUID(s): %v. Service ids are environment-scoped -- resolve services by NAME (as SETTLE_QUERY at :637 already does) so the check works in per-PR environments too.", len(hits), hits)
	}
}

// T2-6. The debug-dump guard. The rendered map is the single most
// credential-dense object this script ever holds, and GITHUB_OUTPUT /
// GITHUB_ENV are the two sinks whose contents outlive the step and flow into
// later steps -- a leak there is durable, not just a scrollback line.
//
// Runs the BAD map: the failure path is where a debug dump is most likely to
// have been added, and it is the path a hurried operator adds one to.
//
// HONESTY NOTE: like T2-5 this cannot fail today for the reason it exists
// (the subcommand does not exist, so nothing is written to either file). Its
// exit-code assertion IS red today; the leak assertions become load-bearing
// only once the executor implements the subcommand. Kept because a leak guard
// added after the leak is a guard added too late.
//
// KILLS: M-echo.
func TestAssertDBDSNsSelfTestDoesNotLeakIntoGitHubFiles(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "github_output")
	envPath := filepath.Join(dir, "github_env")
	for _, p := range []string{outPath, envPath} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatalf("creating %s: %v", p, err)
		}
	}

	stdout, _, code := runSelfTest(t, badSelfTestMap(), []string{
		"GITHUB_OUTPUT=" + outPath,
		"GITHUB_ENV=" + envPath,
	})

	if code == 0 {
		t.Errorf("exit code = 0 on the bad map -- this run must FAIL for the leak assertions below to be meaningful rather than vacuous")
	}
	// NON-VACUITY PRECONDITION -- see T2-2. Without this, a script that never
	// reaches the check writes nothing to either file and passes.
	if !strings.Contains(stdout, "gateway") || !strings.Contains(stdout, "DATABASE_MIGRATION_URL") {
		t.Errorf("the self-test did not report the offender -- the no-leak assertions below are vacuous until it does; stdout = %q", stdout)
	}

	for _, p := range []string{outPath, envPath} {
		body, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("reading %s: %v", p, err)
		}
		text := string(body)
		if strings.Contains(text, sentinelPW) {
			t.Errorf("%s contains the password sentinel %q -- GITHUB_OUTPUT/GITHUB_ENV outlive the step and flow into later ones, so a leak here is durable; contents = %q", filepath.Base(p), sentinelPW, text)
		}
		if strings.Contains(text, "postgresql://") {
			t.Errorf("%s contains a raw DSN; contents = %q", filepath.Base(p), text)
		}
	}

	if strings.Contains(stdout, "postgresql://") {
		t.Errorf("stdout dumped raw DSNs from the rendered map; report service and variable NAMES only. stdout = %q", stdout)
	}
	// A whole-map dump is recognisable by its JSON envelope even if the
	// values were somehow scrubbed.
	if strings.Contains(stdout, `{"gateway":`) || strings.Contains(stdout, `"DATABASE_MIGRATION_URL":`) {
		t.Errorf("stdout dumped the whole rendered map as JSON; stdout = %q", stdout)
	}
}

const dsnForkEnv = "env-fork-dsn"

// dsnExtras hold no DSN: the fleet has 16 instances, the severity table covers 9.
var dsnExtras = []string{"auth", "auth-admin", "app", "docling", "landing", "ops-console", "support-console", "Postgres"}

const dsnUnrelatedNeedle = "n33dle-unrelated-secret"

// dsnFleet is an environment of len(m)+len(dsnExtras) instances in settle order (sorted by name).
// Each service's rendered map is m[svc] plus one unrelated secret; its unrendered store holds
// references only, so a read that sends `unrendered` fails the verdict. A `go` stub on PATH
// keeps what reaches `prenv dsn-check` on stdin in dsn-map.json.
type dsnFleet struct {
	authShim
	order []string
	m     dsnMap
}

func (f dsnFleet) id(svc string) string { return "svc-" + svc }

func newDSNFleet(t *testing.T, m dsnMap) dsnFleet {
	t.Helper()
	all := dsnMap{}
	for svc, vars := range m {
		all[svc] = vars
	}
	for _, svc := range dsnExtras {
		all[svc] = map[string]string{}
	}
	order := slices.Sorted(maps.Keys(all))
	var edges []string
	stores := map[string]map[string]string{}
	for _, svc := range order {
		edges = append(edges, instance("svc-"+svc, svc))
		stores["svc-"+svc] = map[string]string{"DATABASE_URL": "${{Postgres.UNRENDERED}}", "DOCUMENT_BUCKET": "${{Postgres.UNRENDERED}}"}
	}
	s := newAuthShim(t, map[string]string{"settle": forkSettleOf(edges)}, stores)
	for _, svc := range order {
		rendered := map[string]string{"PORT": "8080"}
		for k, v := range all[svc] {
			rendered[k] = v
		}
		if svc == "gateway" {
			rendered["UNRELATED_SECRET"] = dsnUnrelatedNeedle
		}
		raw, err := json.Marshal(rendered)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(s.dir, "rendered-svc-"+svc+"-"+dsnForkEnv+".json"), string(raw))
		writeFile(t, filepath.Join(s.dir, "rendered-svc-"+svc+"-"+persistentEnvironmentID+".json"), string(raw))
	}
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	stub := "#!/bin/sh\n" +
		"if [ \"$1\" = build ]; then\n" +
		"  while [ $# -gt 0 ]; do [ \"$1\" = -o ] && out=\"$2\"; shift; done\n" +
		"  printf '#!/bin/sh\\ntee \"%s\" | \"%s\" \"$@\"\\n' '" + filepath.Join(s.dir, "dsn-map.json") + "' '" + binPath + "' > \"$out\"\n" +
		"  chmod +x \"$out\"; exit 0\n" +
		"fi\n" +
		"exec '" + realGo + "' \"$@\"\n"
	writeFile(t, filepath.Join(s.dir, "go"), stub)
	if err := os.Chmod(filepath.Join(s.dir, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dsnFleet{s, order, all}
}

func forkSettleOf(edges []string) string {
	return `{"data":{"environment":{"serviceInstances":{"edges":[` + strings.Join(edges, ",") + `]}}}}`
}

func (f dsnFleet) run(t *testing.T, args ...string) (out string, code int) {
	t.Helper()
	stdout, stderr, code := f.authShim.run(t, forkExports(true, true, true), "assert-db-dsns", args...)
	return stdout + stderr, code
}

// varsReads returns the varsRead calls in order.
func (f dsnFleet) varsReads(t *testing.T) []railwayCall {
	t.Helper()
	var out []railwayCall
	for _, c := range f.calls(t) {
		if m := gqlOperation.FindStringSubmatch(c.Query); m != nil && m[1] == "varsRead" {
			out = append(out, c)
		}
	}
	return out
}

// sentMap is what the script handed `prenv dsn-check` on stdin.
func (f dsnFleet) sentMap(t *testing.T) dsnMap {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.dir, "dsn-map.json"))
	if err != nil {
		t.Fatalf("dsn-check never received a map: %v", err)
	}
	var m dsnMap
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("the map sent to dsn-check is not JSON: %v", err)
	}
	return m
}

var (
	dsnAliasField = regexp.MustCompile(`(?m)\b(s[0-9]+)\s*:\s*variables\([^)]*serviceId:\s*\$(s[0-9]+)`)
	dsnReportLine = regexp.MustCompile(`DSN check (clean|FAILED)`)
)

// requireTwoCalls asserts the whole call shape: one settle, then one varsRead with one sK
// variable per instance, every one in env and none unrendered.
func requireTwoCalls(t *testing.T, f dsnFleet, env string) {
	t.Helper()
	if len(f.order) != 16 {
		t.Fatalf("fixture: %d instances, want 16", len(f.order))
	}
	if got, want := operations(f.calls(t)), []string{"settle", "varsRead"}; !slices.Equal(got, want) {
		t.Fatalf("Railway calls = %v, want %v: one settle and one batched read", got, want)
	}
	reads := f.varsReads(t)
	if len(reads) != 1 {
		t.Fatalf("varsRead calls = %d, want 1", len(reads))
	}
	for _, c := range f.calls(t) {
		if c.Variables["e"] != env {
			t.Errorf("a %q call asked for environment %v, want %s", gqlOperation.FindStringSubmatch(c.Query)[1], c.Variables["e"], env)
		}
	}
	got := map[string]bool{}
	for k, v := range reads[0].Variables {
		if regexp.MustCompile(`^s[0-9]+$`).MatchString(k) {
			id, _ := v.(string)
			got[id] = true
		}
	}
	for _, svc := range f.order {
		if !got[f.id(svc)] {
			t.Errorf("varsRead has no sK variable for instance %s; variables = %v", f.id(svc), reads[0].Variables)
		}
	}
	if len(got) != 16 {
		t.Errorf("varsRead carries %d sK variables, want 16 (one per instance)", len(got))
	}
	fields := dsnAliasField.FindAllStringSubmatch(reads[0].Query, -1)
	if len(fields) != 16 {
		t.Errorf("the varsRead query has %d aliased variables(...) fields, want 16", len(fields))
	}
	seen := map[string]bool{}
	for _, m := range fields {
		if m[1] != m[2] {
			t.Errorf("alias %s reads serviceId $%s: the alias and its variable must share one index", m[1], m[2])
		}
		if seen[m[1]] {
			t.Errorf("alias %s appears twice in the varsRead query; GraphQL rejects duplicate response keys", m[1])
		}
		seen[m[1]] = true
	}
	if strings.Contains(reads[0].Query, "unrendered") {
		t.Errorf("the varsRead query asks for unrendered variables; the DSN check needs the rendered map:\n%s", reads[0].Query)
	}
}

func TestAssertDBDSNs_ReadsAnEnvironmentInTwoCalls(t *testing.T) {
	f := newDSNFleet(t, healthyMap())
	out, code := f.run(t, dsnForkEnv)
	if code != 0 {
		t.Errorf("exit %d, want 0: every rendered map is healthy; output = %q", code, out)
	}
	requireTwoCalls(t, f, dsnForkEnv)
}

func TestAssertDBDSNs_SourceOnlyReadsThePersistentEnvironmentInTwoCalls(t *testing.T) {
	f := newDSNFleet(t, healthyMap())
	out, code := f.run(t, "--source-only")
	if code != 0 {
		t.Errorf("exit %d, want 0: every rendered map is healthy; output = %q", code, out)
	}
	requireTwoCalls(t, f, persistentEnvironmentID)
}

func TestAssertDBDSNs_BatchedReadFeedsTheSameVerdict(t *testing.T) {
	m := healthyMap()
	m["tenancy"]["DATABASE_URL"] = "postgresql://invoice_app:@" + railwayHost
	f := newDSNFleet(t, m)
	out, code := f.run(t, dsnForkEnv)

	if n := len(f.varsReads(t)); n != 1 {
		t.Fatalf("control: varsRead calls = %d, want 1, so the verdict below does not rest on the batched read; output = %q", n, out)
	}
	if code != 1 {
		t.Errorf("exit %d, want 1: tenancy's DATABASE_URL renders with an empty password; output = %q", code, out)
	}
	if !strings.Contains(out, "DSN check FAILED: 1 defect(s)") || !regexp.MustCompile(`(?m)^  tenancy DATABASE_URL `).MatchString(out) {
		t.Errorf("output lacks dsn-check's failure line for tenancy.DATABASE_URL alone; output = %q", out)
	}
	if strings.Contains(out, "DSN check clean") {
		t.Errorf("output carries the pass report; output = %q", out)
	}
}

func TestAssertDBDSNs_HealthyBatchedReadPasses(t *testing.T) {
	f := newDSNFleet(t, healthyMap())
	out, code := f.run(t, dsnForkEnv)

	if n := len(f.varsReads(t)); n != 1 {
		t.Fatalf("control: varsRead calls = %d, want 1, so the verdict below does not rest on the batched read; output = %q", n, out)
	}
	if code != 0 || !strings.Contains(out, "DSN check clean") {
		t.Errorf("exit %d, want 0 with dsn-check's pass report; output = %q", code, out)
	}
	// The map keeps only DATABASE*/DOCUMENT* variables, one entry per instance, empty for a service with none.
	want := dsnMap{}
	for _, svc := range f.order {
		want[svc] = map[string]string{}
		for k, v := range f.m[svc] {
			want[svc][k] = v
		}
	}
	if got := f.sentMap(t); !mapsEqual(got, want) {
		t.Errorf("the map sent to dsn-check differs from the DATABASE*/DOCUMENT* variables of each rendered map (values withheld); services sent %v, want %v", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
	}
}

func mapsEqual(a, b dsnMap) bool {
	if len(a) != len(b) {
		return false
	}
	for svc, av := range a {
		if bv, ok := b[svc]; !ok || !maps.Equal(av, bv) {
			return false
		}
	}
	return true
}

// failsBeforeTheCheck asserts a failed run that never reached dsn-check, and that it said so.
func failsBeforeTheCheck(t *testing.T, f dsnFleet, out string, code int) {
	t.Helper()
	if n := len(f.varsReads(t)); n != 1 {
		t.Errorf("varsRead calls = %d, want 1: a GraphQL error or a bad alias is not retried", n)
	}
	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, out)
	}
	if dsnReportLine.MatchString(out) || strings.Contains(out, "DB DSN check FAILED") {
		t.Errorf("dsn-check ran: the step must fail before it; output = %q", out)
	}
	if !strings.Contains(out, "NOT evidence") {
		t.Errorf("output lacks \"NOT evidence\"; output = %q", out)
	}
}

func namesService(out, svc string) bool {
	return regexp.MustCompile(`(^|[^A-Za-z0-9-])` + regexp.QuoteMeta(svc) + `([^A-Za-z0-9-]|$)`).MatchString(out)
}

func TestAssertDBDSNs_BatchedReadGraphQLErrorFailsBeforeTheCheck(t *testing.T) {
	const needle = "n33dle-railway-message"
	errBody := func(paths ...string) string {
		var es []string
		for _, p := range paths {
			path := ""
			if p != "" {
				path = `"path":[` + p + `],`
			}
			es = append(es, `{"message":"`+needle+`",`+path+`"extensions":{"code":"INTERNAL_SERVER_ERROR"}}`)
		}
		return `{"errors":[` + strings.Join(es, ",") + `],"data":null}`
	}
	cases := []struct {
		name    string
		plant   func(t *testing.T, f dsnFleet)
		service []int // aliases the error must name
		bystand []int // aliases it must not name
		wantEnv bool
		needles []string
	}{
		{"path names the service of the alias", func(t *testing.T, f dsnFleet) {
			writeFile(t, filepath.Join(f.dir, "varsRead.json"), errBody(`"s3"`))
		}, []int{3}, []int{0, 9}, false, []string{needle}},
		{"two aliased errors name both services", func(t *testing.T, f dsnFleet) {
			writeFile(t, filepath.Join(f.dir, "varsRead.json"), errBody(`"s3"`, `"s7"`))
		}, []int{3, 7}, []int{0, 9}, false, []string{needle}},
		{"no path names the environment", func(t *testing.T, f dsnFleet) {
			writeFile(t, filepath.Join(f.dir, "varsRead.json"), errBody(""))
		}, nil, []int{0, 3, 9}, true, []string{needle}},
		{"a path that is no alias names the environment", func(t *testing.T, f dsnFleet) {
			writeFile(t, filepath.Join(f.dir, "varsRead.json"), errBody(`"variables"`))
		}, nil, []int{0, 3, 9}, true, []string{needle}},
		{"an alias past the request names the environment", func(t *testing.T, f dsnFleet) {
			writeFile(t, filepath.Join(f.dir, "varsRead.json"), errBody(`"s99"`))
		}, nil, []int{0, 3, 9}, true, []string{needle}},
		{"a zero-padded alias past the base-8 digits names the environment", func(t *testing.T, f dsnFleet) {
			writeFile(t, filepath.Join(f.dir, "varsRead.json"), errBody(`"s08"`))
		}, nil, []int{0, 3, 9}, true, []string{needle}},
		{"a numeric path head names the environment", func(t *testing.T, f dsnFleet) {
			writeFile(t, filepath.Join(f.dir, "varsRead.json"), errBody(`3`))
		}, nil, []int{0, 3, 9}, true, []string{needle}},
		{"an in-range and an out-of-range alias name only the real service", func(t *testing.T, f dsnFleet) {
			writeFile(t, filepath.Join(f.dir, "varsRead.json"), errBody(`"s3"`, `"s99"`))
		}, []int{3}, []int{0, 9}, false, []string{needle}},
		{"a gqlerr fault names the environment", func(t *testing.T, f dsnFleet) {
			setFaults(t, f.authShim, "varsRead", "gqlerr")
		}, nil, []int{0, 3, 9}, true, []string{"Not Authorized"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newDSNFleet(t, healthyMap())
			c.plant(t, f)
			out, code := f.run(t, dsnForkEnv)

			failsBeforeTheCheck(t, f, out, code)
			for _, i := range c.service {
				if !namesService(out, f.order[i]) {
					t.Errorf("output does not name %s, the service of alias s%d; output = %q", f.order[i], i, out)
				}
			}
			for _, i := range c.bystand {
				if namesService(out, f.order[i]) {
					t.Errorf("output names %s, whose alias carries no error; output = %q", f.order[i], out)
				}
			}
			if c.wantEnv && !strings.Contains(out, dsnForkEnv) {
				t.Errorf("output does not name the environment %s; output = %q", dsnForkEnv, out)
			}
			for _, n := range c.needles {
				if strings.Contains(out, n) {
					t.Errorf("output carries Railway's message text %q; output = %q", n, out)
				}
			}
		})
	}

	t.Run("an HTTP 400 body never reaches the output", func(t *testing.T) {
		f := newDSNFleet(t, healthyMap())
		writeFile(t, filepath.Join(f.dir, "faultbody-varsRead"), errBody(`"s3"`))
		setFaults(t, f.authShim, "varsRead", "400")
		out, code := f.run(t, dsnForkEnv)

		if n := len(f.varsReads(t)); n != 1 {
			t.Errorf("varsRead calls = %d, want 1: an HTTP 400 is not retried", n)
		}
		if code != 1 {
			t.Errorf("exit %d, want 1; output = %q", code, out)
		}
		if dsnReportLine.MatchString(out) || strings.Contains(out, "DB DSN check FAILED") {
			t.Errorf("dsn-check ran: the step must fail before it; output = %q", out)
		}
		if strings.Contains(out, needle) {
			t.Errorf("output carries Railway's message text from the 400 body; output = %q", out)
		}
		if !strings.Contains(out, dsnForkEnv) {
			t.Errorf("output does not name the environment %s; output = %q", dsnForkEnv, out)
		}
	})
}

func TestAssertDBDSNs_MissingAliasFailsNamingTheService(t *testing.T) {
	const target, bystander = "portfolio", "gateway"
	cases := []struct {
		name  string
		plant func(t *testing.T, f dsnFleet, at int)
	}{
		{"alias null", func(t *testing.T, f dsnFleet, at int) { f.bendRead(t, f.id(target), "null") }},
		{"alias a string", func(t *testing.T, f dsnFleet, at int) { f.bendRead(t, f.id(target), `"oops"`) }},
		{"alias an array", func(t *testing.T, f dsnFleet, at int) { f.bendRead(t, f.id(target), `[]`) }},
		{"alias absent from data", func(t *testing.T, f dsnFleet, at int) {
			data := map[string]any{}
			for i, svc := range f.order {
				if i != at {
					data[fmt.Sprintf("s%d", i)] = f.m[svc]
				}
			}
			raw, err := json.Marshal(map[string]any{"data": data})
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(f.dir, "varsRead.json"), string(raw))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newDSNFleet(t, healthyMap())
			at := slices.Index(f.order, target)
			if at < 0 || !slices.Contains(f.order, bystander) {
				t.Fatalf("fixture: %s or %s is not an instance", target, bystander)
			}
			// The bend reads the store: drop the rendered file the fake serves first.
			if err := os.Remove(filepath.Join(f.dir, "rendered-"+f.id(target)+"-"+dsnForkEnv+".json")); err != nil {
				t.Fatal(err)
			}
			c.plant(t, f, at)
			out, code := f.run(t, dsnForkEnv)

			failsBeforeTheCheck(t, f, out, code)
			if !namesService(out, target) {
				t.Errorf("output does not name %s, the service with the unreadable alias; output = %q", target, out)
			}
			if namesService(out, bystander) {
				t.Errorf("output names %s, whose alias is fine; output = %q", bystander, out)
			}
		})
	}
}

func TestAssertDBDSNs_NoValueOnArgvOrInOutput(t *testing.T) {
	const needle = "n33dle-dsn-password-7f3a"
	withNeedle := func() dsnMap {
		m := dsnMap{}
		for svc, vars := range healthyMap() {
			m[svc] = map[string]string{}
			for k, v := range vars {
				m[svc][k] = strings.ReplaceAll(v, sentinelPW, needle)
			}
		}
		return m
	}
	for _, c := range []struct {
		name string
		m    func() dsnMap
		code int
	}{
		{"a healthy fleet", withNeedle, 0},
		{"a failing fleet", func() dsnMap {
			m := withNeedle()
			m["tenancy"]["DATABASE_URL"] = "postgresql://invoice_app:@" + railwayHost
			return m
		}, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newDSNFleet(t, c.m())
			jqLog := jqArgvLog(t, f.authShim)
			out, code := f.run(t, dsnForkEnv)

			curlArgv, jqArgv := f.argv(t), readLog(t, jqLog)
			if code != c.code || !dsnReportLine.MatchString(out) {
				t.Fatalf("control: exit %d (want %d) with output %q, so the run did not reach dsn-check", code, c.code, out)
			}
			if n := len(f.varsReads(t)); n != 1 {
				t.Fatalf("control: varsRead calls = %d, want 1, so the needles never travelled the batched read", n)
			}
			if curlArgv == "" || jqArgv == "" {
				t.Fatalf("control: curl argv log (%d bytes) or jq argv log (%d) is empty, so a clean scan proves nothing", len(curlArgv), len(jqArgv))
			}
			for label, n := range map[string]string{
				"the DSN password": needle, "the unrelated secret": dsnUnrelatedNeedle,
				"a DOCUMENT_* secret key": sentinelSecret, "a DOCUMENT_* key id": sentinelKeyID,
			} {
				for where, text := range map[string]string{"the output": out, "curl's argv": curlArgv, "jq's argv": jqArgv} {
					if strings.Contains(text, n) {
						t.Errorf("%s carries %s", where, label)
					}
				}
			}
		})
	}
}

func TestAssertDBDSNs_UnusableBatchedResponseFailsBeforeTheCheck(t *testing.T) {
	cases := []struct{ name, body string }{
		{"a 200 that is not JSON", "<html>bad gateway</html>"},
		{"an empty 200", ""},
		{"no data and no errors", `{}`},
		{"data null", `{"data":null}`},
		{"data a string", `{"data":"oops"}`},
		{"a top-level array", `[]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newDSNFleet(t, healthyMap())
			writeFile(t, filepath.Join(f.dir, "varsRead.json"), c.body)
			out, code := f.run(t, dsnForkEnv)

			failsBeforeTheCheck(t, f, out, code)
			if !strings.Contains(out, dsnForkEnv) {
				t.Errorf("output does not name the environment %s; output = %q", dsnForkEnv, out)
			}
		})
	}
}

func TestAssertDBDSNs_ExtraAliasInDataIsIgnored(t *testing.T) {
	const needle = "n33dle-extra-alias"
	f := newDSNFleet(t, healthyMap())
	data := map[string]any{}
	for i, svc := range f.order {
		data[fmt.Sprintf("s%d", i)] = f.m[svc]
	}
	data["s99"] = map[string]string{"DATABASE_URL": "postgresql://x:" + needle + "@" + railwayHost}
	data["extra"] = map[string]string{"DATABASE_URL": needle}
	raw, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(f.dir, "varsRead.json"), string(raw))
	out, code := f.run(t, dsnForkEnv)

	if code != 0 || !strings.Contains(out, "DSN check clean") {
		t.Fatalf("exit %d, want 0 with the pass report: an alias this request never sent is not a service; output = %q", code, out)
	}
	got := f.sentMap(t)
	if len(got) != len(f.order) {
		t.Errorf("dsn-check received %d services, want %d: %v", len(got), len(f.order), slices.Sorted(maps.Keys(got)))
	}
	if b, _ := json.Marshal(got); strings.Contains(string(b), needle) {
		t.Errorf("the map sent to dsn-check carries a value from an alias the request never sent")
	}
}

func TestAssertDBDSNs_BatchedReadTransportFailureNamesTheEnvironment(t *testing.T) {
	cases := []struct {
		name   string
		faults []string
		reads  int
	}{
		{"three timeouts", []string{"timeout", "timeout", "timeout"}, 3},
		{"three 503s", []string{"503", "503", "503"}, 3},
		{"a 401 is not retried", []string{"401"}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newDSNFleet(t, healthyMap())
			setFaults(t, f.authShim, "varsRead", c.faults...)
			out, code := f.run(t, dsnForkEnv)

			if n := len(f.varsReads(t)); n != c.reads {
				t.Errorf("varsRead calls = %d, want %d", n, c.reads)
			}
			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, out)
			}
			if dsnReportLine.MatchString(out) || strings.Contains(out, "DB DSN check FAILED") {
				t.Errorf("dsn-check ran: the step must fail before it; output = %q", out)
			}
			if !strings.Contains(out, "NOT evidence") || !strings.Contains(out, dsnForkEnv) {
				t.Errorf("output lacks \"NOT evidence\" or the environment %s; output = %q", dsnForkEnv, out)
			}
		})
	}
}

func TestAssertDBDSNs_NoInstanceFailsBeforeAnyRead(t *testing.T) {
	f := newDSNFleet(t, healthyMap())
	writeFile(t, filepath.Join(f.dir, "settle.json"), forkSettleOf(nil))
	out, code := f.run(t, dsnForkEnv)

	if got := operations(f.calls(t)); !slices.Equal(got, []string{"settle"}) {
		t.Errorf("Railway calls = %v, want only the settle read", got)
	}
	if code != 1 || !strings.Contains(out, "NOT evidence") {
		t.Errorf("exit %d, want 1 with \"NOT evidence\"; output = %q", code, out)
	}
	if strings.Contains(out, "unbound variable") {
		t.Errorf("a shell error reached the output; output = %q", out)
	}
}
