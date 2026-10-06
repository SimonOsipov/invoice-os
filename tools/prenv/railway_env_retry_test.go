// railway_env_retry_test.go drives railway-env.sh's Railway transport against a faulting curl:
// what it retries, what it fails on at once, and what it prints.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	retryForkEnv    = "env-fork-retry"
	retryGatewayID  = "svc-gw-retry"
	retryAppID      = "svc-app-retry"
	retryLandingID  = "svc-landing-retry"
	retryOpsID      = "svc-ops-retry"
	retrySupportID  = "svc-support-retry"
	retryPostgresID = "svc-pg-retry"
	retryForkBucket = "source-documents-fork-1a2b"

	// Contexts copied from railway-env.sh.
	sealedAuditCtx = "auditing sealed variables in the source environment " + persistentEnvironmentID // cmd_audit_sealed_variables
	envListCtx     = "listing environments in project " + forkProjectID                               // fetch_environment_list

	gqlNotAuthorized = `{"errors":[{"message":"Not Authorized","extensions":{"code":"INTERNAL_SERVER_ERROR"}}]}`
	gqlProblem       = `{"errors":[{"message":"Problem processing request","extensions":{"code":"BAD_USER_INPUT"}}]}`
	// Measured on PR #263 (story INFRA-02, [premise-no-429]).
	gqlTooQuickly = `{"errors":[{"message":"You are creating environments too quickly. This workspace allows 1 environment per 30 seconds"}]}`
)

var curlResetMsg = regexp.MustCompile(`\(56\)|Failure when receiving data from the peer`)

// setFaults queues one outcome per call of op: timeout, reset, gqlerr or an HTTP code.
func setFaults(t *testing.T, s authShim, op string, outcomes ...string) {
	t.Helper()
	writeFile(t, filepath.Join(s.dir, "faults-"+op), strings.Join(outcomes, " ")+"\n")
}

func opCount(t *testing.T, s authShim, op string) int {
	t.Helper()
	n := 0
	for _, o := range operations(s.calls(t)) {
		if o == op {
			n++
		}
	}
	return n
}

// opCountIn counts calls of op whose variables.e is env.
func opCountIn(t *testing.T, s authShim, op, env string) int {
	t.Helper()
	n := 0
	for _, c := range s.calls(t) {
		if m := gqlOperation.FindStringSubmatch(c.Query); m != nil && m[1] == op && c.Variables["e"] == env {
			n++
		}
	}
	return n
}

func warningLines(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, "::warning::") {
			out = append(out, l)
		}
	}
	return out
}

func retryExports() string {
	return forkExports(true, true, true) +
		"export RAILWAY_SVC_GATEWAY_ID=" + retryGatewayID + "\n" +
		"export RAILWAY_SVC_APP_ID=" + retryAppID + "\n" +
		"export RAILWAY_SVC_LANDING_ID=" + retryLandingID + "\n" +
		"export RAILWAY_SVC_OPS_CONSOLE_ID=" + retryOpsID + "\n" +
		"export RAILWAY_SVC_SUPPORT_CONSOLE_ID=" + retrySupportID + "\n" +
		"export RAILWAY_SVC_POSTGRES_ID=" + retryPostgresID + "\n"
}

func domainsOf(custom bool, host string) string {
	d := `[{"id":"dom-` + host + `","domain":"` + host + `","targetPort":8080,"syncStatus":"ACTIVE"}]`
	if custom {
		return `{"data":{"domains":{"customDomains":` + d + `,"serviceDomains":[]}}}`
	}
	return `{"data":{"domains":{"customDomains":[],"serviceDomains":` + d + `}}}`
}

const noDomains = `{"data":{"domains":{"customDomains":[],"serviceDomains":[]}}}`

func volumesOf(serviceIDs ...string) string {
	var edges []string
	for _, id := range serviceIDs {
		edges = append(edges, `{"node":{"id":"vol-`+id+`","serviceId":"`+id+`","mountPath":"/var/lib/postgresql/data","sizeMB":5000,"currentSizeMB":1,"state":"READY","region":"europe-west4"}}`)
	}
	return `{"data":{"environment":{"volumeInstances":{"edges":[` + strings.Join(edges, ",") + `]}}}}`
}

func postgresInstance(status string) string {
	dep := "null"
	if status != "NONE" {
		dep = `{"id":"dep-pg","status":"` + status + `","createdAt":"2026-09-29T00:00:00Z"}`
	}
	return `{"data":{"serviceInstance":{"serviceName":"Postgres","latestDeployment":` + dep + `}}}`
}

func bucketNamed(name string) string {
	return `{"data":{"bucketS3Credentials":[{"bucketName":"` + name + `"}]}}`
}

func retrySettle(withPostgres bool) string {
	edges := []string{instance(retryGatewayID, "gateway"), instance(retryAppID, "app"), instance(retryLandingID, "landing"),
		instance(retryOpsID, "ops-console"), instance(retrySupportID, "support-console")}
	if withPostgres {
		edges = append(edges, instance(retryPostgresID, "Postgres"))
	}
	return forkSettle(edges...)
}

// reconcileForkFixture is a reused fork on which reconcile-fork mutates nothing and exits 0.
func reconcileForkFixture(t *testing.T) authShim {
	t.Helper()
	src := persistentEnvironmentID
	r := map[string]string{
		"settle":                      retrySettle(true),
		"vols-" + retryForkEnv:        volumesOf(retryPostgresID),
		"vols-" + src:                 volumesOf(retryPostgresID),
		"svcInstance":                 postgresInstance("SUCCESS"),
		"svcDeploy":                   `{"data":{"serviceInstanceDeployV2":"dep-pg-new"}}`,
		"svcRedeploy":                 `{"data":{"serviceInstanceRedeploy":true}}`,
		"domCreate":                   `{"data":{"serviceDomainCreate":{"id":"dom-new","domain":"new.up.railway.app","targetPort":8080}}}`,
		"volCreate":                   `{"data":{"volumeCreate":{"id":"vol-new","name":"pg","createdAt":"2026-09-29T00:00:00Z"}}}`,
		"staged":                      `{"data":{"environment":{"id":"` + retryForkEnv + `","name":"pr-900","unmergedChangesCount":0}}}`,
		"buckets":                     `{"data":{"project":{"buckets":{"edges":[{"node":{"id":"bkt-docs","name":"source-documents"}}],"pageInfo":{"hasNextPage":false}}}}}`,
		"bucketCreate":                `{"data":{"bucketCreate":{"id":"bkt-docs","name":"source-documents"}}}`,
		"bucketCreds-" + retryForkEnv: bucketNamed(retryForkBucket),
		"bucketCreds-" + src:          bucketNamed("source-documents-dev-9z8y"),
	}
	for _, svc := range []string{retryGatewayID, retryAppID, retryLandingID, retryOpsID, retrySupportID} {
		r["dom-"+retryForkEnv+"-"+svc] = domainsOf(false, svc+"-pr-900.up.railway.app")
		r["dom-"+src+"-"+svc] = domainsOf(true, svc+".ascomply.com")
	}
	return newAuthShim(t, r, nil)
}

func runReconcileFork(t *testing.T, s authShim) (stdout, stderr string, code int) {
	t.Helper()
	return s.run(t, retryExports(), "reconcile-fork", retryForkEnv)
}

func TestRailwayAPI_ReadTimeoutThenSuccess(t *testing.T) {
	s := newSealedShim(t, sourceInstances(), plainOn("PORT", sealedGatewayID))
	setFaults(t, s, "sealedAudit", "timeout")
	stdout, stderr, code := s.run(t, forkExports(true, true, true), "audit-sealed-variables")

	if code != 0 {
		t.Errorf("exit %d, want 0: a read that timed out once is retried; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "sealedAudit"); n != 2 {
		t.Errorf("sealedAudit calls = %d, want 2", n)
	}
	if got := s.sleeps(t); !slices.Equal(got, []string{"5"}) {
		t.Errorf("sleeps = %v, want [5]", got)
	}
	if !strings.Contains(stdout, "Sealed-variable audit clean:") {
		t.Errorf("stdout lacks the clean-audit line; stdout = %q", stdout)
	}
}

func TestRailwayAPI_HTTP5xxThenSuccess(t *testing.T) {
	for _, status := range []string{"500", "502", "503", "504"} {
		t.Run(status, func(t *testing.T) {
			m := healthyMap()
			var edges []string
			stores := map[string]map[string]string{}
			for svc, vars := range m {
				edges = append(edges, instance("svc-"+svc, svc))
				stores["svc-"+svc] = vars
			}
			s := newAuthShim(t, map[string]string{"settle": `{"data":{"environment":{"serviceInstances":{"edges":[` + strings.Join(edges, ",") + `]}}}}`}, stores)
			setFaults(t, s, "svcVars", status)
			stdout, stderr, code := s.run(t, forkExports(true, true, true), "assert-db-dsns", retryForkEnv)

			if code != 0 {
				t.Errorf("exit %d, want 0: an HTTP %s on a read is retried; output = %q", code, status, stdout+stderr)
			}
			if !strings.Contains(stdout, "DSN check clean") {
				t.Errorf("stdout lacks the DSN report; stdout = %q", stdout)
			}
			if n := opCount(t, s, "svcVars"); n != len(m)+1 {
				t.Errorf("svcVars calls = %d, want %d (one per service plus the retry)", n, len(m)+1)
			}
		})
	}
}

func TestRailwayAPI_WriteTimeoutThenSuccess(t *testing.T) {
	s := newAuthShim(t, map[string]string{
		"settle": forkSettle(`{"node":{"serviceId":"` + productionGatewayID + `","serviceName":"gateway"}}`),
	}, map[string]map[string]string{
		productionGatewayID: {"RAILWAY_ENVIRONMENT_NAME": "production", "ENVIRONMENT": "development"},
	})
	setFaults(t, s, "varUpsert", "timeout")
	stdout, stderr, code := s.run(t, forkExports(true, true, true), "set-production-environment", persistentEnvironmentID)

	if code != 0 {
		t.Errorf("exit %d, want 0: an idempotent write that timed out once is retried; output = %q", code, stdout+stderr)
	}
	var inputs []any
	for _, c := range s.calls(t) {
		if strings.Contains(c.Query, "variableUpsert(") {
			inputs = append(inputs, c.Variables["input"])
		}
	}
	if len(inputs) != 2 {
		t.Fatalf("varUpsert calls = %d, want 2", len(inputs))
	}
	if !reflect.DeepEqual(inputs[0], inputs[1]) {
		t.Errorf("the retry sent a different input: %v then %v", inputs[0], inputs[1])
	}
	if !strings.Contains(stdout, productionConfirmation) {
		t.Errorf("stdout lacks %q; stdout = %q", productionConfirmation, stdout)
	}
}

func TestRailwayAPI_SecretWriteTimeoutThenSuccess(t *testing.T) {
	jwk := freshJWK(t)
	s := newProdAuthShim(t, prodSealed())
	// The first write of --post-merge is auth.GOTRUE_JWT_KEYS, through upsert_secret_variable.
	setFaults(t, s, "varUpsert", "timeout")
	stdout, stderr, code := s.run(t, prodAuthExports(authProdPassword, jwk, authProdJWTSecret, ""), "set-production-auth", "--post-merge", persistentEnvironmentID)
	out := stdout + stderr

	if code != 0 {
		t.Errorf("exit %d, want 0: a secret write that timed out once is retried; output = %q", code, out)
	}
	if n := len(upsertsOf(s.upserts(t), authProdAuthID, "GOTRUE_JWT_KEYS")); n != 2 {
		t.Errorf("auth.GOTRUE_JWT_KEYS upserts = %d, want 2", n)
	}
	w := warningLines(stderr)
	if len(w) != 1 || !strings.Contains(w[0], "GOTRUE_JWT_KEYS") {
		t.Errorf("stderr warnings = %q, want one naming GOTRUE_JWT_KEYS", w)
	}
	secretScan(t, s, out, map[string]string{
		"the planted JWK": jwk, "its private scalar": jwkPrivateScalar(t, jwk),
		"the admin password": authProdPassword, "the JWT secret": authProdJWTSecret,
	})
}

func TestRailwayAPI_RetrySuccessPrintsOneWarningOnStderr(t *testing.T) {
	control := newSealedShim(t, sourceInstances(), plainOn("PORT", sealedGatewayID))
	want, _, code := control.run(t, forkExports(true, true, true), "audit-sealed-variables")
	if code != 0 || want == "" {
		t.Fatalf("control: fault-free run exit %d, stdout %q", code, want)
	}

	s := newSealedShim(t, sourceInstances(), plainOn("PORT", sealedGatewayID))
	setFaults(t, s, "sealedAudit", "503")
	stdout, stderr, code := s.run(t, forkExports(true, true, true), "audit-sealed-variables")

	if code != 0 {
		t.Errorf("exit %d, want 0; output = %q", code, stdout+stderr)
	}
	w := warningLines(stderr)
	if len(w) != 1 {
		t.Fatalf("stderr warnings = %q, want exactly one", w)
	}
	for _, needle := range []string{sealedAuditCtx, "attempt 2/3"} {
		if !strings.Contains(w[0], needle) {
			t.Errorf("the warning lacks %q: %q", needle, w[0])
		}
	}
	for _, body := range []string{`"data"`, "isSealed", "edges"} {
		if strings.Contains(w[0], body) {
			t.Errorf("the warning carries response body text %q: %q", body, w[0])
		}
	}
	if stdout != want {
		t.Errorf("stdout differs from a fault-free run:\n got %q\nwant %q", stdout, want)
	}
}

func TestListEnvironments_RetryKeepsStdoutTSV(t *testing.T) {
	s := newAuthShim(t, map[string]string{"envList": authEnvList(true)}, nil)
	setFaults(t, s, "envList", "timeout")
	stdout, stderr, code := s.run(t, forkExports(true, true, false), "list-environments")

	if code != 0 {
		t.Errorf("exit %d, want 0; output = %q", code, stdout+stderr)
	}
	want := "production\tfalse\t" + persistentEnvironmentID + "\n" +
		"pr-7\ttrue\t" + authForkEnvID + "\n" +
		"pr-8\ttrue\t" + authStaleEnvID + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want exactly the TSV rows %q", stdout, want)
	}
	w := warningLines(stderr)
	if len(w) != 1 || !strings.Contains(w[0], envListCtx) {
		t.Errorf("stderr warnings = %q, want one naming %q", w, envListCtx)
	}
}

func TestVerifyGatewayDomain_RetryKeepsTheDiscoveredURL(t *testing.T) {
	host := "gateway-pr-900.up.railway.app"
	s := newAuthShim(t, map[string]string{"dom-" + retryForkEnv + "-" + retryGatewayID: domainsOf(false, host)}, nil)
	writeFile(t, filepath.Join(s.dir, "probe.body"), `{"status":"ok"}`)
	writeFile(t, filepath.Join(s.dir, "probe.code"), "200")
	setFaults(t, s, "dom", "503")
	genv := filepath.Join(t.TempDir(), "github_env")
	stdout, stderr, code := s.run(t, retryExports()+"export GITHUB_ENV='"+genv+"'\n", "verify-gateway-domain", retryForkEnv)

	if code != 0 {
		t.Errorf("exit %d, want 0; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "dom"); n != 2 {
		t.Errorf("dom calls = %d, want 2", n)
	}
	raw, err := os.ReadFile(genv)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if want := "GATEWAY_URL=https://" + host + "\n"; string(raw) != want {
		t.Errorf("GITHUB_ENV = %q, want exactly %q", raw, want)
	}
}

func TestRailwayAPI_ExhaustedBudgetNamesRailwayCallAndLastError(t *testing.T) {
	s := newSealedShim(t, sourceInstances(), plainOn("PORT", sealedGatewayID))
	setFaults(t, s, "sealedAudit", "timeout", "timeout", "timeout")
	stdout, stderr, code := s.run(t, forkExports(true, true, true), "audit-sealed-variables")

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "sealedAudit"); n != 3 {
		t.Errorf("sealedAudit calls = %d, want 3", n)
	}
	e := errorLines(stderr)
	for _, needle := range []string{"Railway", sealedAuditCtx, "(28)"} {
		if !strings.Contains(e, needle) {
			t.Errorf("stderr error lines lack %q: %q", needle, e)
		}
	}
	if got := s.sleeps(t); !slices.Equal(got, []string{"5", "10"}) {
		t.Errorf("sleeps = %v, want [5 10]", got)
	}
}

func TestRailwayAPI_ExhaustedOn5xxNamesTheStatus(t *testing.T) {
	for _, faults := range [][]string{{"503", "503", "503"}, {"timeout", "timeout", "503"}} {
		t.Run(strings.Join(faults, "_"), func(t *testing.T) {
			s := newSealedShim(t, sourceInstances(), plainOn("PORT", sealedGatewayID))
			setFaults(t, s, "sealedAudit", faults...)
			stdout, stderr, code := s.run(t, forkExports(true, true, true), "audit-sealed-variables")

			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
			}
			if n := opCount(t, s, "sealedAudit"); n != 3 {
				t.Errorf("sealedAudit calls = %d, want 3", n)
			}
			// The last error is the 503; an earlier timeout must not stand in for it.
			if e := errorLines(stdout + stderr); !strings.Contains(e, "503") || strings.Contains(e, "(28)") {
				t.Errorf("error lines do not name the last error, 503, alone: %q", e)
			}
		})
	}
}

// guard, passes at HEAD: a GraphQL error is fatal on the first call.
func TestRailwayAPI_GraphQLErrorIsNotRetried(t *testing.T) {
	s := newAuthShim(t, map[string]string{"sealed": `{"errors":[{"message":"boom","extensions":{"code":"BAD_USER_INPUT"}}]}`}, nil)
	stdout, stderr, code := s.run(t, forkExports(true, true, true), "audit-sealed-variables")

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "sealedAudit"); n != 1 {
		t.Errorf("sealedAudit calls = %d, want 1", n)
	}
	if e := errorLines(stdout + stderr); !strings.Contains(e, "boom") {
		t.Errorf("error lines do not carry the GraphQL message: %q", e)
	}
	if got := s.sleeps(t); len(got) != 0 {
		t.Errorf("sleeps = %v, want none", got)
	}
}

// guard, passes at HEAD: "Not Authorized" is a GraphQL error, fatal on the first call.
// A Retry-After on a 200 is not a wait: only an HTTP 429 sends a call again.
func TestRailwayAPI_NotAuthorizedIsNotRetried(t *testing.T) {
	s := newSealedShim(t, sourceInstances(), plainOn("PORT", sealedGatewayID))
	setFaults(t, s, "sealedAudit", "gqlerr")
	writeFile(t, filepath.Join(s.dir, "hdr.txt"), "HTTP/2 200\r\nretry-after: 5\r\n\r\n")
	stdout, stderr, code := s.run(t, forkExports(true, true, true), "audit-sealed-variables")

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "sealedAudit"); n != 1 {
		t.Errorf("sealedAudit calls = %d, want 1", n)
	}
	if e := errorLines(stdout + stderr); !strings.Contains(e, "Not Authorized") {
		t.Errorf("error lines do not carry Not Authorized: %q", e)
	}
}

// envListWithout lists the persistent environment only, so pr-900 is absent.
func envListWithout() string {
	return `{"data":{"environments":{"edges":[{"node":{"id":"` + persistentEnvironmentID + `","name":"production","isEphemeral":false}}]}}}`
}

// ensureEnvironmentFailsOnce runs ensure-environment pr-900 with envList faulted three times.
func ensureEnvironmentFailsOnce(t *testing.T, fault string, headers ...string) (s authShim, errors string) {
	t.Helper()
	s = newAuthShim(t, map[string]string{"envList": envListWithout()}, nil)
	setFaults(t, s, "envList", fault, fault, fault)
	if len(headers) > 0 {
		plantRateLimitHeaders(t, s, headers...)
	}
	stdout, stderr, code := s.run(t, forkExports(true, true, true), "ensure-environment", "pr-900")

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "envList"); n != 1 {
		t.Errorf("envList calls = %d, want 1: %s is not retried", n, fault)
	}
	if n := opCount(t, s, "createPrEnvironment"); n != 0 {
		t.Errorf("createPrEnvironment calls = %d, want 0", n)
	}
	return s, errorLines(stdout + stderr)
}

func TestRailwayAPI_HTTP4xxIsNotRetried(t *testing.T) {
	for _, c := range []string{"400", "401", "403"} {
		t.Run(c, func(t *testing.T) {
			_, e := ensureEnvironmentFailsOnce(t, c)
			if !strings.Contains(e, "HTTP "+c) {
				t.Errorf("error lines do not name HTTP %s: %q", c, e)
			}
		})
		t.Run(c+" with a Retry-After", func(t *testing.T) {
			s, e := ensureEnvironmentFailsOnce(t, c, "retry-after: 5")
			if !strings.Contains(e, "HTTP "+c) {
				t.Errorf("error lines do not name HTTP %s: %q", c, e)
			}
			if got := s.sleeps(t); len(got) != 0 {
				t.Errorf("sleeps = %v, want none: only an HTTP 429 waits", got)
			}
		})
	}
}

func TestRailwayAPI_RetryAfterOnA5xxIsNotTheWait(t *testing.T) {
	s := rateLimitedEnvList(t, []string{"retry-after: 7"}, "503")
	stdout, stderr, code := ensurePR(t, s, "")

	if code != 0 {
		t.Errorf("exit %d, want 0: a 5xx is a transient fault; output = %q", code, stdout+stderr)
	}
	if got := s.sleeps(t); !slices.Equal(got, []string{"5"}) {
		t.Errorf("sleeps = %v, want [5]: the transient backoff, not the Retry-After", got)
	}
}

var noWaitMsg = regexp.MustCompile(`(?i)no wait`)

// envListWithPR lists pr-900 as ephemeral, so ensure-environment reuses it after one read.
func envListWithPR() string {
	return `{"data":{"environments":{"edges":[{"node":{"id":"` + persistentEnvironmentID + `","name":"production","isEphemeral":false}},` +
		`{"node":{"id":"env-pr-900","name":"pr-900","isEphemeral":true}}]}}}`
}

// plantRateLimitHeaders makes every response of the shim carry a CRLF header dump, as curl -D writes it.
func plantRateLimitHeaders(t *testing.T, s authShim, lines ...string) {
	t.Helper()
	writeFile(t, filepath.Join(s.dir, "hdr.txt"), "HTTP/2 429\r\n"+strings.Join(lines, "\r\n")+"\r\n\r\n")
}

// resetAt is an ISO-8601 UTC time d from now, in whole seconds.
func resetAt(d time.Duration) string {
	return time.Now().UTC().Add(d).Format("2006-01-02T15:04:05Z")
}

// rateLimitedEnvList is a shim whose envList answers faults, then lists pr-900.
func rateLimitedEnvList(t *testing.T, headers []string, faults ...string) authShim {
	t.Helper()
	s := newAuthShim(t, map[string]string{"envList": envListWithPR()}, nil)
	setFaults(t, s, "envList", faults...)
	if len(headers) > 0 {
		plantRateLimitHeaders(t, s, headers...)
	}
	return s
}

func ensurePR(t *testing.T, s authShim, extraExports string) (stdout, stderr string, code int) {
	t.Helper()
	return s.run(t, forkExports(true, true, true)+extraExports, "ensure-environment", "pr-900")
}

func runnerTempExport(dir string) string { return "export RUNNER_TEMP='" + dir + "'\n" }

func callLogRows(t *testing.T, dir string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "railway-api-calls.tsv"))
	if err != nil {
		t.Fatalf("the call log was not written: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func waitedTotal(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "railway-api-429-waited"))
	if err != nil {
		t.Fatalf("the 429 total was not written: %v", err)
	}
	return strings.TrimSpace(string(raw))
}

// sleepsWithin asserts exactly one sleep, with an integer argument in [lo, hi].
// lo is about half the planted wait: a loaded runner can delay the script by tens of seconds.
func sleepsWithin(t *testing.T, got []string, lo, hi int) {
	t.Helper()
	if len(got) != 1 {
		t.Errorf("sleeps = %v, want exactly one in [%d, %d]", got, lo, hi)
		return
	}
	if n, err := strconv.Atoi(got[0]); err != nil || n < lo || n > hi {
		t.Errorf("sleeps = %v, want one in [%d, %d]", got, lo, hi)
	}
}

// sentTwiceIdentically asserts op was sent twice with the same body.
func sentTwiceIdentically(t *testing.T, s authShim, op string) {
	t.Helper()
	var sent []railwayCall
	for _, c := range s.calls(t) {
		if m := gqlOperation.FindStringSubmatch(c.Query); m != nil && m[1] == op {
			sent = append(sent, c)
		}
	}
	if len(sent) != 2 {
		t.Errorf("%s calls = %d, want 2", op, len(sent))
		return
	}
	if !reflect.DeepEqual(sent[0], sent[1]) {
		t.Errorf("the retry of %s sent a different body: %v then %v", op, sent[0], sent[1])
	}
}

func TestRailwayAPI_RateLimitWaitsRetryAfterThenRetriesOnce(t *testing.T) {
	s := rateLimitedEnvList(t, []string{"retry-after: 30"}, "429")
	stdout, stderr, code := ensurePR(t, s, "")

	if code != 0 {
		t.Errorf("exit %d, want 0: a 429 waits Retry-After and retries; output = %q", code, stdout+stderr)
	}
	if got := s.sleeps(t); !slices.Equal(got, []string{"30"}) {
		t.Errorf("sleeps = %v, want [30]", got)
	}
	sentTwiceIdentically(t, s, "envList")
	w := warningLines(stderr)
	if len(w) != 1 {
		t.Fatalf("stderr warnings = %q, want exactly one", w)
	}
	for _, needle := range []string{"429", "30"} {
		if !strings.Contains(w[0], needle) {
			t.Errorf("the warning lacks %q: %q", needle, w[0])
		}
	}
}

func TestRailwayAPI_RateLimitWaitOfZeroRetriesAtOnce(t *testing.T) {
	s := rateLimitedEnvList(t, []string{"retry-after: 0"}, "429")
	stdout, stderr, code := ensurePR(t, s, "")

	if code != 0 {
		t.Errorf("exit %d, want 0; output = %q", code, stdout+stderr)
	}
	if got := s.sleeps(t); !slices.Equal(got, []string{"0"}) {
		t.Errorf("sleeps = %v, want [0]", got)
	}
	if n := opCount(t, s, "envList"); n != 2 {
		t.Errorf("envList calls = %d, want 2", n)
	}
}

func TestRailwayAPI_SecondRateLimitFailsNamingTheWait(t *testing.T) {
	s := rateLimitedEnvList(t, []string{"retry-after: 30"}, "429", "429")
	stdout, stderr, code := ensurePR(t, s, "")

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "envList"); n != 2 {
		t.Errorf("envList calls = %d, want 2: one retry, no more", n)
	}
	if got := s.sleeps(t); !slices.Equal(got, []string{"30"}) {
		t.Errorf("sleeps = %v, want [30]", got)
	}
	e := errorLines(stdout + stderr)
	for _, re := range []string{`429`, `(?i)second`, `\b30\b`} {
		if !regexp.MustCompile(re).MatchString(e) {
			t.Errorf("error lines do not match %s: %q", re, e)
		}
	}
}

func TestRailwayAPI_RateLimitWaitOfSixHundredIsHonoured(t *testing.T) {
	s := rateLimitedEnvList(t, []string{"retry-after: 600"}, "429")
	stdout, stderr, code := ensurePR(t, s, "")

	if code != 0 {
		t.Errorf("exit %d, want 0: 600 s is the limit, not over it; output = %q", code, stdout+stderr)
	}
	if got := s.sleeps(t); !slices.Equal(got, []string{"600"}) {
		t.Errorf("sleeps = %v, want [600]", got)
	}
}

func TestRailwayAPI_RateLimitOverSixHundredFailsAtOnce(t *testing.T) {
	s := rateLimitedEnvList(t, []string{"retry-after: 601"}, "429")
	stdout, stderr, code := ensurePR(t, s, "")

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "envList"); n != 1 {
		t.Errorf("envList calls = %d, want 1", n)
	}
	e := errorLines(stdout + stderr)
	for _, re := range []string{`429`, `\b601\b`, `\b600\b`} {
		if !regexp.MustCompile(re).MatchString(e) {
			t.Errorf("error lines do not match %s: %q", re, e)
		}
	}
	if got := s.sleeps(t); len(got) != 0 {
		t.Errorf("sleeps = %v, want none: a wait over the limit is not slept", got)
	}
	if strings.Contains(e, "already waited") {
		t.Errorf("the per-call limit must fire before the job total: %q", e)
	}
}

func TestRailwayAPI_RateLimitFallsBackToXRateLimitReset(t *testing.T) {
	for name, reset := range map[string]string{
		"whole second":      resetAt(120 * time.Second),
		"fractional second": strings.TrimSuffix(resetAt(120*time.Second), "Z") + ".123Z",
	} {
		t.Run(name, func(t *testing.T) {
			s := rateLimitedEnvList(t, []string{"x-ratelimit-reset: " + reset}, "429")
			stdout, stderr, code := ensurePR(t, s, "")

			if code != 0 {
				t.Errorf("exit %d, want 0; output = %q", code, stdout+stderr)
			}
			sleepsWithin(t, s.sleeps(t), 60, 121)
		})
	}
}

func TestRailwayAPI_RateLimitResetInThePastRetriesAtOnce(t *testing.T) {
	s := rateLimitedEnvList(t, []string{"x-ratelimit-reset: " + resetAt(-60*time.Second)}, "429")
	stdout, stderr, code := ensurePR(t, s, "")

	if code != 0 {
		t.Errorf("exit %d, want 0; output = %q", code, stdout+stderr)
	}
	if got := s.sleeps(t); !slices.Equal(got, []string{"0"}) {
		t.Errorf("sleeps = %v, want [0]: a wait floors at 0", got)
	}
}

func TestRailwayAPI_RateLimitResetOverSixHundredFailsAtOnce(t *testing.T) {
	s := rateLimitedEnvList(t, []string{"x-ratelimit-reset: " + resetAt(900*time.Second)}, "429")
	stdout, stderr, code := ensurePR(t, s, "")

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "envList"); n != 1 {
		t.Errorf("envList calls = %d, want 1", n)
	}
	e := errorLines(stdout + stderr)
	for _, re := range []string{`429`, `wait of [6-9][0-9]{2} s`, `\b600\b`} {
		if !regexp.MustCompile(re).MatchString(e) {
			t.Errorf("error lines do not match %s: %q", re, e)
		}
	}
	if got := s.sleeps(t); len(got) != 0 {
		t.Errorf("sleeps = %v, want none", got)
	}
}

func TestRailwayAPI_RateLimitWithNoUsableWaitFailsAtOnce(t *testing.T) {
	cases := []struct {
		name    string
		headers []string
	}{
		{"no headers", nil},
		{"retry-after as an HTTP date", []string{"retry-after: Wed, 21 Oct 2026 07:28:00 GMT"}},
		{"unparseable reset", []string{"x-ratelimit-reset: garbage"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := rateLimitedEnvList(t, c.headers, "429")
			stdout, stderr, code := ensurePR(t, s, "")

			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
			}
			if n := opCount(t, s, "envList"); n != 1 {
				t.Errorf("envList calls = %d, want 1", n)
			}
			e := errorLines(stdout + stderr)
			if !strings.Contains(e, "429") || !noWaitMsg.MatchString(e) {
				t.Errorf("error lines do not name HTTP 429 and say Railway gave no wait: %q", e)
			}
			if got := s.sleeps(t); len(got) != 0 {
				t.Errorf("sleeps = %v, want none", got)
			}
		})
	}
}

func TestRailwayAPI_RateLimitResendsAOnceMutationAfterTheWait(t *testing.T) {
	s := newAuthShim(t, map[string]string{
		"createPrEnvironment": `{"data":{"environmentCreate":{"id":"env-pr-900","name":"pr-900","isEphemeral":true}}}`,
	}, nil)
	// The lookup sees no pr-900; the re-query after the create sees it.
	writeFile(t, filepath.Join(s.dir, "envList.seq"), compactJSON(t, envListWithout())+"\n"+compactJSON(t, envListWithPR())+"\n")
	setFaults(t, s, "createPrEnvironment", "429")
	plantRateLimitHeaders(t, s, "retry-after: 5")
	stdout, stderr, code := ensurePR(t, s, "")

	if code != 0 {
		t.Errorf("exit %d, want 0: Railway did not run a call it answered with 429; output = %q", code, stdout+stderr)
	}
	sentTwiceIdentically(t, s, "createPrEnvironment")
	if got := s.sleeps(t); len(got) == 0 || got[0] != "5" {
		t.Errorf("sleeps = %v, want the first to be 5", got)
	}
}

func compactJSON(t *testing.T, s string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestRailwayAPI_RateLimitWaitKeepsTheTransientBudget(t *testing.T) {
	const cmd = "ensure-environment"
	s := rateLimitedEnvList(t, []string{"retry-after: 5"}, "429", "timeout", "timeout")
	tmp := t.TempDir()
	stdout, stderr, code := ensurePR(t, s, runnerTempExport(tmp))

	if code != 0 {
		t.Errorf("exit %d, want 0: the wait spends no transient attempt; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "envList"); n != 4 {
		t.Errorf("envList calls = %d, want 4", n)
	}
	if got := s.sleeps(t); !slices.Equal(got, []string{"5", "5", "10"}) {
		t.Errorf("sleeps = %v, want [5 5 10]", got)
	}
	want := []string{cmd + "\t1\tratelimit", cmd + "\t2\ttransient", cmd + "\t3\ttransient", cmd + "\t4\tok"}
	if got := callLogRows(t, tmp); !slices.Equal(got, want) {
		t.Errorf("call log = %q, want %q", got, want)
	}
	var succeeded []string
	for _, w := range warningLines(stderr) {
		if strings.Contains(w, "succeeded on attempt") {
			succeeded = append(succeeded, w)
		}
	}
	if len(succeeded) != 1 || !strings.Contains(succeeded[0], "attempt 3/3") {
		t.Errorf("success warnings = %q, want one naming attempt 3/3", succeeded)
	}
}

func TestRailwayAPI_RateLimitAfterATransientFault(t *testing.T) {
	const cmd = "ensure-environment"
	t.Run("then success", func(t *testing.T) {
		s := rateLimitedEnvList(t, []string{"retry-after: 5"}, "timeout", "429")
		tmp := t.TempDir()
		stdout, stderr, code := ensurePR(t, s, runnerTempExport(tmp))

		if code != 0 {
			t.Errorf("exit %d, want 0; output = %q", code, stdout+stderr)
		}
		if got := s.sleeps(t); !slices.Equal(got, []string{"5", "5"}) {
			t.Errorf("sleeps = %v, want [5 5]", got)
		}
		want := []string{cmd + "\t1\ttransient", cmd + "\t2\tratelimit", cmd + "\t3\tok"}
		if got := callLogRows(t, tmp); !slices.Equal(got, want) {
			t.Errorf("call log = %q, want %q", got, want)
		}
		var succeeded []string
		for _, w := range warningLines(stderr) {
			if strings.Contains(w, "succeeded on attempt") {
				succeeded = append(succeeded, w)
			}
		}
		if len(succeeded) != 1 || !strings.Contains(succeeded[0], "attempt 2/3") {
			t.Errorf("success warnings = %q, want one naming attempt 2/3", succeeded)
		}
	})
	t.Run("a second 429 fails", func(t *testing.T) {
		s := rateLimitedEnvList(t, []string{"retry-after: 5"}, "429", "timeout", "429")
		stdout, stderr, code := ensurePR(t, s, "")

		if code != 1 {
			t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
		}
		if n := opCount(t, s, "envList"); n != 3 {
			t.Errorf("envList calls = %d, want 3", n)
		}
		if got := s.sleeps(t); !slices.Equal(got, []string{"5", "5"}) {
			t.Errorf("sleeps = %v, want [5 5]", got)
		}
		if e := errorLines(stdout + stderr); !strings.Contains(e, "429") || !regexp.MustCompile(`(?i)second`).MatchString(e) {
			t.Errorf("error lines do not name the second 429: %q", e)
		}
	})
}

func TestRailwayAPI_RateLimitHeadersAreReadInAnyCaseWithCRLF(t *testing.T) {
	t.Run("Retry-After", func(t *testing.T) {
		s := rateLimitedEnvList(t, nil, "429")
		writeFile(t, filepath.Join(s.dir, "hdr.txt"), "HTTP/2 429\r\nRetry-After: 30\r\n\r\n")
		stdout, stderr, code := ensurePR(t, s, "")

		if code != 0 {
			t.Errorf("exit %d, want 0: a CRLF line end does not spoil the value; output = %q", code, stdout+stderr)
		}
		if got := s.sleeps(t); !slices.Equal(got, []string{"30"}) {
			t.Errorf("sleeps = %v, want [30]", got)
		}
	})
	t.Run("mixed case, padded value", func(t *testing.T) {
		s := rateLimitedEnvList(t, nil, "429")
		writeFile(t, filepath.Join(s.dir, "hdr.txt"), "HTTP/2 429\r\nrEtRy-AfTeR:   30  \r\n\r\n")
		stdout, stderr, code := ensurePR(t, s, "")

		if code != 0 {
			t.Errorf("exit %d, want 0; output = %q", code, stdout+stderr)
		}
		if got := s.sleeps(t); !slices.Equal(got, []string{"30"}) {
			t.Errorf("sleeps = %v, want [30]", got)
		}
	})
	t.Run("LF line ends", func(t *testing.T) {
		s := rateLimitedEnvList(t, nil, "429")
		writeFile(t, filepath.Join(s.dir, "hdr.txt"), "HTTP/2 429\nretry-after: 30\n\n")
		stdout, stderr, code := ensurePR(t, s, "")

		if code != 0 {
			t.Errorf("exit %d, want 0; output = %q", code, stdout+stderr)
		}
		if got := s.sleeps(t); !slices.Equal(got, []string{"30"}) {
			t.Errorf("sleeps = %v, want [30]", got)
		}
	})
	t.Run("x-RaTeLiMiT-ReSeT alone", func(t *testing.T) {
		s := rateLimitedEnvList(t, nil, "429")
		writeFile(t, filepath.Join(s.dir, "hdr.txt"), "HTTP/2 429\r\nx-RaTeLiMiT-ReSeT: "+resetAt(120*time.Second)+"\r\n\r\n")
		stdout, stderr, code := ensurePR(t, s, "")

		if code != 0 {
			t.Errorf("exit %d, want 0; output = %q", code, stdout+stderr)
		}
		sleepsWithin(t, s.sleeps(t), 60, 121)
	})
	t.Run("X-RATELIMIT-RESET alone", func(t *testing.T) {
		s := rateLimitedEnvList(t, nil, "429")
		writeFile(t, filepath.Join(s.dir, "hdr.txt"), "HTTP/2 429\r\nX-RATELIMIT-RESET: "+resetAt(120*time.Second)+"\r\n\r\n")
		stdout, stderr, code := ensurePR(t, s, "")

		if code != 0 {
			t.Errorf("exit %d, want 0; output = %q", code, stdout+stderr)
		}
		sleepsWithin(t, s.sleeps(t), 60, 121)
	})
}

func TestRailwayAPI_UnusableRetryAfterFallsBackToTheReset(t *testing.T) {
	for _, bad := range []string{
		"retry-after: -5", "retry-after: 1.5", "retry-after: +5", "retry-after: 5s", "retry-after: 0x10",
		"retry-after:", "retry-after: Wed, 21 Oct 2026 07:28:00 GMT",
	} {
		t.Run(bad+" with a reset", func(t *testing.T) {
			s := rateLimitedEnvList(t, []string{bad, "x-ratelimit-reset: " + resetAt(60*time.Second)}, "429")
			stdout, stderr, code := ensurePR(t, s, "")

			if code != 0 {
				t.Errorf("exit %d, want 0; output = %q", code, stdout+stderr)
			}
			sleepsWithin(t, s.sleeps(t), 30, 61)
		})
		t.Run(bad+" without a reset", func(t *testing.T) {
			s := rateLimitedEnvList(t, []string{bad}, "429")
			stdout, stderr, code := ensurePR(t, s, "")

			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
			}
			if n := opCount(t, s, "envList"); n != 1 {
				t.Errorf("envList calls = %d, want 1", n)
			}
			if e := errorLines(stdout + stderr); !strings.Contains(e, "429") || !noWaitMsg.MatchString(e) {
				t.Errorf("error lines do not name HTTP 429 and say Railway gave no wait: %q", e)
			}
			if got := s.sleeps(t); len(got) != 0 {
				t.Errorf("sleeps = %v, want none", got)
			}
		})
	}
}

func TestRailwayAPI_RetryAfterWinsOverTheReset(t *testing.T) {
	s := rateLimitedEnvList(t, []string{"retry-after: 7", "x-ratelimit-reset: " + resetAt(300*time.Second)}, "429")
	stdout, stderr, code := ensurePR(t, s, "")

	if code != 0 {
		t.Errorf("exit %d, want 0; output = %q", code, stdout+stderr)
	}
	if got := s.sleeps(t); !slices.Equal(got, []string{"7"}) {
		t.Errorf("sleeps = %v, want [7]", got)
	}
}

func TestRailwayAPI_RateLimitWaitsAreCappedPerJob(t *testing.T) {
	for _, c := range []struct {
		name, waited string
		code         int
		sleeps       []string
		file, shown  string // shown is the total the error names
	}{
		{"590 s waited, 30 s asked", "590", 1, nil, "590", "590"},
		{"570 s waited, 30 s asked", "570", 0, []string{"30"}, "600", ""},
		{"571 s waited, 30 s asked", "571", 1, nil, "571", "571"},
		{"600 s waited, 1 s asked", "600", 1, nil, "600", "600"},
		{"leading zero is decimal, over", "0590", 1, nil, "0590", "590"},
		{"leading zero is decimal, under", "0570", 0, []string{"30"}, "600", ""},
		{"a non-numeric total counts as 0", "garbage", 0, []string{"30"}, "30", ""},
		{"a negative total counts as 0", "-590", 0, []string{"30"}, "30", ""},
		{"an empty total counts as 0", "", 0, []string{"30"}, "30", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			wait := "retry-after: 30"
			if c.waited == "600" {
				wait = "retry-after: 1"
			}
			s := rateLimitedEnvList(t, []string{wait}, "429")
			tmp := t.TempDir()
			writeFile(t, filepath.Join(tmp, "railway-api-429-waited"), c.waited+"\n")
			stdout, stderr, code := ensurePR(t, s, runnerTempExport(tmp))

			if code != c.code {
				t.Errorf("exit %d, want %d; output = %q", code, c.code, stdout+stderr)
			}
			if got := s.sleeps(t); !slices.Equal(got, c.sleeps) {
				t.Errorf("sleeps = %v, want %v", got, c.sleeps)
			}
			if got := waitedTotal(t, tmp); got != c.file {
				t.Errorf("railway-api-429-waited = %q, want %q", got, c.file)
			}
			if c.code == 1 {
				e := errorLines(stdout + stderr)
				asked := strings.TrimPrefix(wait, "retry-after: ")
				for _, re := range []string{`429`, `\b` + asked + `\b`, `\b` + c.shown + `\b`, `\b600\b`} {
					if !regexp.MustCompile(re).MatchString(e) {
						t.Errorf("error lines do not match %s: %q", re, e)
					}
				}
			}
		})
	}

	// Two calls of one process: the first waits 400 s, the second would pass the total.
	for _, c := range []struct {
		name       string
		runnerTemp bool
	}{
		{"per process without RUNNER_TEMP", false},
		{"across calls through RUNNER_TEMP", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newAuthShim(t, map[string]string{"envList": envListWithout()}, nil)
			setFaults(t, s, "envList", "429")
			setFaults(t, s, "createPrEnvironment", "429")
			plantRateLimitHeaders(t, s, "retry-after: 400")
			exports, tmp := "", ""
			if c.runnerTemp {
				tmp = t.TempDir()
				exports = runnerTempExport(tmp)
			}
			stdout, stderr, code := ensurePR(t, s, exports)

			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
			}
			if n := opCount(t, s, "createPrEnvironment"); n != 1 {
				t.Errorf("createPrEnvironment calls = %d, want 1: the second wait would pass the total", n)
			}
			got := 0
			for _, sl := range s.sleeps(t) {
				if sl == "400" {
					got++
				}
			}
			if got != 1 {
				t.Errorf("sleeps = %v, want exactly one 400", s.sleeps(t))
			}
			e := errorLines(stdout + stderr)
			if n := strings.Count(e, "400"); n < 2 {
				t.Errorf("error lines name 400 %d time(s), want the wait and the seconds already waited: %q", n, e)
			}
			if !regexp.MustCompile(`\b600\b`).MatchString(e) {
				t.Errorf("error lines do not name the 600 s total: %q", e)
			}
			if c.runnerTemp {
				if got := waitedTotal(t, tmp); got != "400" {
					t.Errorf("railway-api-429-waited = %q, want 400: the refused wait is not added", got)
				}
			}
		})
	}
}

func TestRailwayAPI_RetryAfterIntegerForms(t *testing.T) {
	for _, c := range []struct {
		value  string
		sleeps []string // nil: the call fails at once
		named  string   // the wait the error names
	}{
		{"08", []string{"8"}, ""},
		{"0030", []string{"30"}, ""},
		{"000", []string{"0"}, ""},
		{"0600", []string{"600"}, ""},
		{"0000000000000000000030", []string{"30"}, ""},
		{"0601", nil, "601"},
		{"99999999999999999999", nil, "99999999999999999999"},
		{"18446744073709551646", nil, "18446744073709551646"}, // wraps to 30 in 64-bit shell arithmetic
	} {
		t.Run(c.value, func(t *testing.T) {
			s := rateLimitedEnvList(t, []string{"retry-after: " + c.value}, "429")
			stdout, stderr, code := ensurePR(t, s, "")

			if c.sleeps == nil {
				if code != 1 {
					t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
				}
				if n := opCount(t, s, "envList"); n != 1 {
					t.Errorf("envList calls = %d, want 1", n)
				}
				if got := s.sleeps(t); len(got) != 0 {
					t.Errorf("sleeps = %v, want none", got)
				}
				e := errorLines(stdout + stderr)
				for _, re := range []string{`429`, `\b` + c.named + `\b`, `\b600\b`} {
					if !regexp.MustCompile(re).MatchString(e) {
						t.Errorf("error lines do not match %s: %q", re, e)
					}
				}
				return
			}
			if code != 0 {
				t.Errorf("exit %d, want 0; output = %q", code, stdout+stderr)
			}
			if got := s.sleeps(t); !slices.Equal(got, c.sleeps) {
				t.Errorf("sleeps = %v, want %v", got, c.sleeps)
			}
		})
	}
}

func TestRailwayAPI_RateLimitResetForms(t *testing.T) {
	t.Run("a nine-digit fraction", func(t *testing.T) {
		reset := strings.TrimSuffix(resetAt(120*time.Second), "Z") + ".123456789Z"
		s := rateLimitedEnvList(t, []string{"x-ratelimit-reset: " + reset}, "429")
		stdout, stderr, code := ensurePR(t, s, "")

		if code != 0 {
			t.Errorf("exit %d, want 0; output = %q", code, stdout+stderr)
		}
		sleepsWithin(t, s.sleeps(t), 60, 121)
	})
	t.Run("the epoch is the past", func(t *testing.T) {
		s := rateLimitedEnvList(t, []string{"x-ratelimit-reset: 1970-01-01T00:00:00Z"}, "429")
		stdout, stderr, code := ensurePR(t, s, "")

		if code != 0 {
			t.Errorf("exit %d, want 0; output = %q", code, stdout+stderr)
		}
		if got := s.sleeps(t); !slices.Equal(got, []string{"0"}) {
			t.Errorf("sleeps = %v, want [0]", got)
		}
	})
	t.Run("the year 9999 is over the limit", func(t *testing.T) {
		s := rateLimitedEnvList(t, []string{"x-ratelimit-reset: 9999-12-31T23:59:59Z"}, "429")
		stdout, stderr, code := ensurePR(t, s, "")

		if code != 1 {
			t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
		}
		if got := s.sleeps(t); len(got) != 0 {
			t.Errorf("sleeps = %v, want none", got)
		}
		if e := errorLines(stdout + stderr); !strings.Contains(e, "429") || !regexp.MustCompile(`\b600\b`).MatchString(e) {
			t.Errorf("error lines do not name HTTP 429 and the 600 s limit: %q", e)
		}
	})
	for name, reset := range map[string]string{
		"a UTC offset":        strings.TrimSuffix(resetAt(120*time.Second), "Z") + "+00:00",
		"a lowercase z":       strings.TrimSuffix(resetAt(120*time.Second), "Z") + "z",
		"a space for the T":   strings.Replace(resetAt(120*time.Second), "T", " ", 1),
		"no time":             time.Now().UTC().Format("2006-01-02"),
		"an impossible month": "2026-13-45T25:61:61Z",
		"epoch seconds":       strconv.FormatInt(time.Now().Unix()+120, 10),
		"a bare fraction dot": strings.TrimSuffix(resetAt(120*time.Second), "Z") + ".Z",
	} {
		t.Run(name+" is no wait", func(t *testing.T) {
			s := rateLimitedEnvList(t, []string{"x-ratelimit-reset: " + reset}, "429")
			stdout, stderr, code := ensurePR(t, s, "")

			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
			}
			if n := opCount(t, s, "envList"); n != 1 {
				t.Errorf("envList calls = %d, want 1", n)
			}
			if got := s.sleeps(t); len(got) != 0 {
				t.Errorf("sleeps = %v, want none", got)
			}
			if e := errorLines(stdout + stderr); !strings.Contains(e, "429") || !noWaitMsg.MatchString(e) {
				t.Errorf("error lines do not name HTTP 429 and say Railway gave no wait: %q", e)
			}
		})
	}
}

func TestRailwayAPI_RateLimitOnAOnceMutationAroundATimeout(t *testing.T) {
	create := map[string]string{
		"createPrEnvironment": `{"data":{"environmentCreate":{"id":"env-pr-900","name":"pr-900","isEphemeral":true}}}`,
	}
	absentThenPresent := func(t *testing.T, s authShim, absent int) {
		t.Helper()
		var rows []string
		for i := 0; i < absent; i++ {
			rows = append(rows, compactJSON(t, envListWithout()))
		}
		rows = append(rows, compactJSON(t, envListWithPR()))
		writeFile(t, filepath.Join(s.dir, "envList.seq"), strings.Join(rows, "\n")+"\n")
	}

	t.Run("a timeout, then a 429 on the next create", func(t *testing.T) {
		s := newAuthShim(t, create, nil)
		absentThenPresent(t, s, 2)
		setFaults(t, s, "createPrEnvironment", "timeout", "429")
		plantRateLimitHeaders(t, s, "retry-after: 5")
		stdout, stderr, code := ensurePR(t, s, "")

		if code != 0 {
			t.Errorf("exit %d, want 0; output = %q", code, stdout+stderr)
		}
		if n := opCount(t, s, "createPrEnvironment"); n != 3 {
			t.Errorf("createPrEnvironment calls = %d, want 3: the timeout, the 429 and its one resend", n)
		}
		if got := s.sleeps(t); !slices.Equal(got, []string{"15", "5", "10"}) {
			t.Errorf("sleeps = %v, want [15 5 10]: the 429 of the second create is its own call", got)
		}
	})
	t.Run("a 429, then a timeout on the resend", func(t *testing.T) {
		s := newAuthShim(t, create, nil)
		absentThenPresent(t, s, 1)
		setFaults(t, s, "createPrEnvironment", "429", "timeout")
		plantRateLimitHeaders(t, s, "retry-after: 5")
		stdout, stderr, code := ensurePR(t, s, "")

		if code != 0 {
			t.Errorf("exit %d, want 0 after the adopt re-query; output = %q", code, stdout+stderr)
		}
		if n := opCount(t, s, "createPrEnvironment"); n != 2 {
			t.Errorf("createPrEnvironment calls = %d, want 2: a once call gets no transient retry after the wait", n)
		}
		if got := s.sleeps(t); !slices.Equal(got, []string{"5", "15"}) {
			t.Errorf("sleeps = %v, want [5 15]", got)
		}
	})
	t.Run("two 429s fail the create and adopt nothing", func(t *testing.T) {
		s := newAuthShim(t, create, nil)
		absentThenPresent(t, s, 5)
		setFaults(t, s, "createPrEnvironment", "429", "429")
		plantRateLimitHeaders(t, s, "retry-after: 5")
		stdout, stderr, code := ensurePR(t, s, "")

		if code != 1 {
			t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
		}
		if n := opCount(t, s, "createPrEnvironment"); n != 2 {
			t.Errorf("createPrEnvironment calls = %d, want 2", n)
		}
		if out := stdout + stderr; !regexp.MustCompile(`(?i)second`).MatchString(out) || !strings.Contains(out, "429") {
			t.Errorf("output does not name the second HTTP 429: %q", out)
		}
	})
}

func TestRailwayAPI_AStaleWaitIsNotReusedByANewCall(t *testing.T) {
	s := newAuthShim(t, map[string]string{"envList": envListWithout()}, nil)
	// The first sleep removes the headers: the second call's 429 carries none.
	stub := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + filepath.Join(s.dir, "sleep.log") + "'\nrm -f '" + filepath.Join(s.dir, "hdr.txt") + "'\n"
	writeFile(t, filepath.Join(s.dir, "sleep"), stub)
	if err := os.Chmod(filepath.Join(s.dir, "sleep"), 0o755); err != nil {
		t.Fatal(err)
	}
	setFaults(t, s, "envList", "429")
	setFaults(t, s, "createPrEnvironment", "429")
	plantRateLimitHeaders(t, s, "retry-after: 5")
	stdout, stderr, code := ensurePR(t, s, "")

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "createPrEnvironment"); n != 1 {
		t.Errorf("createPrEnvironment calls = %d, want 1: its 429 gave no wait", n)
	}
	if got := s.sleeps(t); !slices.Equal(got, []string{"5", "15"}) {
		t.Errorf("sleeps = %v, want [5 15]: the first call's wait once, then the adopt pause; no reused wait", got)
	}
	if e := errorLines(stdout + stderr); !noWaitMsg.MatchString(e) {
		t.Errorf("error lines do not say Railway gave no wait: %q", e)
	}
}

func TestRailwayAPI_ConnectionResetIsNotRetried(t *testing.T) {
	_, e := ensureEnvironmentFailsOnce(t, "reset")
	if !curlResetMsg.MatchString(e) {
		t.Errorf("error lines do not carry curl's reset message: %q", e)
	}
}

func TestEnvironmentListGraphQLErrorFailsOnFirstOccurrence(t *testing.T) {
	s := newAuthShim(t, map[string]string{"envList": gqlProblem}, nil)
	stdout, stderr, code := s.run(t, forkExports(true, true, true), "ensure-environment", "pr-900")

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "envList"); n != 1 {
		t.Errorf("envList calls = %d, want 1: a GraphQL error is not repeated", n)
	}
	if n := opCount(t, s, "createPrEnvironment"); n != 0 {
		t.Errorf("createPrEnvironment calls = %d, want 0", n)
	}
}

func TestSettleForkMissingServiceFailsOnFirstRead(t *testing.T) {
	s := reconcileForkFixture(t)
	writeFile(t, filepath.Join(s.dir, "settle.json"), retrySettle(false))
	stdout, stderr, code := runReconcileFork(t, s)

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "settle"); n != 1 {
		t.Errorf("settle calls = %d, want 1: a missing service is not polled for", n)
	}
	if e := errorLines(stdout + stderr); !strings.Contains(e, retryPostgresID) {
		t.Errorf("error lines do not name the missing id %s: %q", retryPostgresID, e)
	}
	if got := s.sleeps(t); len(got) != 0 {
		t.Errorf("sleeps = %v, want none", got)
	}
}

func TestBucketIsolationSourceReadErrorFailsOnFirstOccurrence(t *testing.T) {
	s := reconcileForkFixture(t)
	writeFile(t, filepath.Join(s.dir, "bucketCreds-"+persistentEnvironmentID+".json"), gqlProblem)
	stdout, stderr, code := runReconcileFork(t, s)

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
	}
	if n := opCountIn(t, s, "bucketCreds", persistentEnvironmentID); n != 1 {
		t.Errorf("source bucketCreds calls = %d, want 1: a GraphQL error is not repeated", n)
	}
	if e := errorLines(stdout + stderr); !strings.Contains(e, "UNVERIFIED") {
		t.Errorf("error lines do not say isolation is UNVERIFIED: %q", e)
	}
}

func TestEnvironmentCreateGraphQLErrorStopsAfterOneRound(t *testing.T) {
	s := newAuthShim(t, map[string]string{"envList": envListWithout(), "createPrEnvironment": gqlTooQuickly}, nil)
	stdout, stderr, code := s.run(t, forkExports(true, true, true), "ensure-environment", "pr-900")

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "createPrEnvironment"); n != 1 {
		t.Errorf("createPrEnvironment calls = %d, want 1: a GraphQL error gets no second round", n)
	}
	if n := opCount(t, s, "envList"); n != 2 {
		t.Errorf("envList calls = %d, want 2 (lookup, then the one re-query)", n)
	}
	if e := errorLines(stdout + stderr); !strings.Contains(e, "creating environments too quickly") {
		t.Errorf("error lines do not name the GraphQL message: %q", e)
	}
}

// guard, passes at HEAD: a create that timed out keeps its two rounds.
func TestEnvironmentCreateTimeoutKeepsTwoRounds(t *testing.T) {
	s := newAuthShim(t, map[string]string{"envList": envListWithout(), "createPrEnvironment": `{"data":{"environmentCreate":{"id":"env-new","name":"pr-900","isEphemeral":true}}}`}, nil)
	setFaults(t, s, "createPrEnvironment", "timeout", "timeout")
	stdout, stderr, code := s.run(t, forkExports(true, true, true), "ensure-environment", "pr-900")

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "createPrEnvironment"); n != 2 {
		t.Errorf("createPrEnvironment calls = %d, want 2", n)
	}
}

func TestDeleteEnvironmentGraphQLErrorStopsAfterOneRound(t *testing.T) {
	list := `{"data":{"environments":{"edges":[` +
		`{"node":{"id":"` + persistentEnvironmentID + `","name":"production","isEphemeral":false}},` +
		`{"node":{"id":"env-pr-900","name":"pr-900","isEphemeral":true}}]}}}`
	s := newAuthShim(t, map[string]string{"envList": list, "deletePrEnvironment": gqlProblem}, nil)
	stdout, stderr, code := s.run(t, forkExports(true, true, true), "delete-environment", "pr-900")

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "deletePrEnvironment"); n != 1 {
		t.Errorf("deletePrEnvironment calls = %d, want 1: a GraphQL error gets no second round", n)
	}
	if n := opCount(t, s, "envList"); n != 2 {
		t.Errorf("envList calls = %d, want 2 (lookup, then the one re-query)", n)
	}
}

// postgresNeverDeployedFixture takes reconcile-fork to wait_for_postgres polling deployment dep-pg-new.
func postgresNeverDeployedFixture(t *testing.T) authShim {
	t.Helper()
	s := reconcileForkFixture(t)
	writeFile(t, filepath.Join(s.dir, "svcInstance.json"), postgresInstance("NONE"))
	return s
}

func TestWaitForPostgres_GraphQLErrorEndsThePoll(t *testing.T) {
	s := postgresNeverDeployedFixture(t)
	writeFile(t, filepath.Join(s.dir, "dep.json"), `{"errors":[{"message":"Deployment not found"}]}`)
	stdout, stderr, code := runReconcileFork(t, s)

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "svcDeploy"); n != 1 {
		t.Fatalf("control: svcDeploy calls = %d, want 1, so the poll was never reached; output = %q", n, stdout+stderr)
	}
	if n := opCount(t, s, "dep"); n != 1 {
		t.Errorf("dep calls = %d, want 1: a GraphQL error ends the poll", n)
	}
	if e := errorLines(stdout + stderr); !strings.Contains(e, "Deployment not found") {
		t.Errorf("error lines do not name the GraphQL message: %q", e)
	}
}

func TestWaitForPostgres_ThirdTransientTickEndsThePoll(t *testing.T) {
	s := postgresNeverDeployedFixture(t)
	writeFile(t, filepath.Join(s.dir, "dep.json"), `{"data":{"deployment":{"id":"dep-pg-new","status":"BUILDING"}}}`)
	setFaults(t, s, "dep", "timeout", "timeout", "timeout")
	stdout, stderr, code := runReconcileFork(t, s)

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "dep"); n != 3 {
		t.Errorf("dep calls = %d, want 3: the poll ends at its 3rd transient tick", n)
	}
	e := errorLines(stdout + stderr)
	for _, needle := range []string{"Railway", "(28)"} {
		if !strings.Contains(e, needle) {
			t.Errorf("error lines lack %q: %q", needle, e)
		}
	}
}

// guard, passes at HEAD: the bucket probe reads "Not Authorized" as "no instance yet".
func TestBucketConfirm_NotAuthorizedKeepsPolling(t *testing.T) {
	s := reconcileForkFixture(t)
	writeFile(t, filepath.Join(s.dir, "bucketCreds-"+retryForkEnv+".seq"), gqlNotAuthorized+"\n"+gqlNotAuthorized+"\n"+bucketNamed(retryForkBucket)+"\n")
	stdout, stderr, code := runReconcileFork(t, s)

	if code != 0 {
		t.Errorf("exit %d, want 0; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "bucketCreate"); n != 1 {
		t.Errorf("bucketCreate calls = %d, want 1", n)
	}
	if n := opCountIn(t, s, "bucketCreds", retryForkEnv); n != 3 {
		t.Errorf("fork bucketCreds calls = %d, want 3 (confirmed on the 3rd probe)", n)
	}
	if !strings.Contains(stdout, "bucket isolation confirmed") {
		t.Errorf("stdout lacks the isolation confirmation; stdout = %q", stdout)
	}
}

// gatewayDomainCreateTimesOut makes the fork's gateway domain read empty first and times out domCreate.
func gatewayDomainCreateTimesOut(t *testing.T, reads ...string) authShim {
	t.Helper()
	s := reconcileForkFixture(t)
	writeFile(t, filepath.Join(s.dir, "dom-"+retryForkEnv+"-"+retryGatewayID+".seq"), strings.Join(reads, "\n")+"\n")
	setFaults(t, s, "domCreate", "timeout")
	return s
}

func TestDomainCreateTimeoutAdoptsTheDomain(t *testing.T) {
	s := gatewayDomainCreateTimesOut(t, noDomains, domainsOf(false, "gateway-adopted.up.railway.app"))
	stdout, stderr, code := runReconcileFork(t, s)
	out := stdout + stderr

	if code != 0 {
		t.Errorf("exit %d, want 0: the re-query adopts the domain; output = %q", code, out)
	}
	if n := opCount(t, s, "domCreate"); n != 1 {
		t.Errorf("domCreate calls = %d, want 1: serviceDomainCreate is sent once", n)
	}
	if !strings.Contains(stdout, "Fork reconciliation complete") {
		t.Errorf("reconcile-fork did not pass the domain step; stdout = %q", stdout)
	}
	adopted := false
	for _, w := range warningLines(out) {
		adopted = adopted || strings.Contains(w, "gateway")
	}
	if !adopted {
		t.Errorf("no ::warning:: names the gateway domain adopted after the timeout; output = %q", out)
	}
}

func TestDomainCreateTimeoutWithNoDomainFails(t *testing.T) {
	s := gatewayDomainCreateTimesOut(t, noDomains, noDomains)
	writeFile(t, filepath.Join(s.dir, "dom-"+retryForkEnv+"-"+retryGatewayID+".json"), noDomains)
	stdout, stderr, code := runReconcileFork(t, s)

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "domCreate"); n != 1 {
		t.Errorf("domCreate calls = %d, want 1", n)
	}
	if e := errorLines(stdout + stderr); !regexp.MustCompile(`\(28\)|timed out`).MatchString(e) {
		t.Errorf("error lines do not name the create's timeout: %q", e)
	}
}

func TestVolumeCreateTimeoutAdoptsTheVolume(t *testing.T) {
	s := reconcileForkFixture(t)
	writeFile(t, filepath.Join(s.dir, "vols-"+retryForkEnv+".seq"), volumesOf()+"\n"+volumesOf(retryPostgresID)+"\n")
	setFaults(t, s, "volCreate", "timeout")
	stdout, stderr, code := runReconcileFork(t, s)

	if code != 0 {
		t.Errorf("exit %d, want 0: the confirm poll adopts the volume; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "volCreate"); n != 1 {
		t.Errorf("volCreate calls = %d, want 1: volumeCreate is sent once", n)
	}
	if !strings.Contains(stdout, "Fork reconciliation complete") {
		t.Errorf("reconcile-fork did not pass the volume step; stdout = %q", stdout)
	}
}

func TestRailwayAPI_BodyNeverOnCurlArgv(t *testing.T) {
	jwk := freshJWK(t)
	s := newProdAuthShim(t, prodSealed())
	stdout, stderr, code := s.run(t, prodAuthExports(authProdPassword, jwk, authProdJWTSecret, ""), "set-production-auth", "--post-merge", persistentEnvironmentID)
	if code != 0 {
		t.Fatalf("control: exit %d, want 0; output = %q", code, stdout+stderr)
	}
	argv := s.argv(t)
	if !strings.Contains(argv, "--data @-") {
		t.Errorf("no call sent its body on stdin (--data @-); argv = %q", argv)
	}
	if strings.Contains(argv, "--data {") {
		t.Error("a request body reached curl's argv (--data {)")
	}
	secretScan(t, s, stdout+stderr, map[string]string{
		"the planted JWK": jwk, "its private scalar": jwkPrivateScalar(t, jwk),
		"the admin password": authProdPassword, "the JWT secret": authProdJWTSecret,
	})
}

func TestRailwayAPI_CallLogOneLinePerAttempt(t *testing.T) {
	const cmd = "audit-sealed-variables"
	for _, c := range []struct {
		name   string
		faults []string
		code   int
		want   []string
		hdr    []string
	}{
		{"transient then ok", []string{"timeout"}, 0, []string{cmd + "\t1\ttransient", cmd + "\t2\tok"}, nil},
		{"exhausted", []string{"503", "timeout", "503"}, 1, []string{cmd + "\t1\ttransient", cmd + "\t2\ttransient", cmd + "\t3\ttransient"}, nil},
		{"fatal", []string{"gqlerr"}, 1, []string{cmd + "\t1\tfatal"}, nil},
		{"429 then ok", []string{"429"}, 0, []string{cmd + "\t1\tratelimit", cmd + "\t2\tok"}, []string{"retry-after: 1"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newSealedShim(t, sourceInstances(), plainOn("PORT", sealedGatewayID))
			setFaults(t, s, "sealedAudit", c.faults...)
			if len(c.hdr) > 0 {
				plantRateLimitHeaders(t, s, c.hdr...)
			}
			tmp := t.TempDir()
			stdout, stderr, code := s.run(t, forkExports(true, true, true)+"export RUNNER_TEMP='"+tmp+"'\n", cmd)

			if code != c.code {
				t.Errorf("exit %d, want %d; output = %q", code, c.code, stdout+stderr)
			}
			raw, err := os.ReadFile(filepath.Join(tmp, "railway-api-calls.tsv"))
			if err != nil {
				t.Fatalf("the call log was not written: %v", err)
			}
			if got := strings.Split(strings.TrimSpace(string(raw)), "\n"); !slices.Equal(got, c.want) {
				t.Errorf("call log = %q, want %q", got, c.want)
			}
		})
	}
}

func TestRailwayAPI_WriteExhaustedBudgetExitsWithoutReRead(t *testing.T) {
	s := newAuthShim(t, map[string]string{
		"settle": forkSettle(`{"node":{"serviceId":"` + productionGatewayID + `","serviceName":"gateway"}}`),
	}, map[string]map[string]string{
		productionGatewayID: {"RAILWAY_ENVIRONMENT_NAME": "production", "ENVIRONMENT": "development"},
	})
	setFaults(t, s, "varUpsert", "timeout", "502", "timeout")
	stdout, stderr, code := s.run(t, forkExports(true, true, true), "set-production-environment", persistentEnvironmentID)

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "varUpsert"); n != 3 {
		t.Errorf("varUpsert calls = %d, want 3", n)
	}
	if n := opCount(t, s, "svcVars"); n != 0 {
		t.Errorf("svcVars calls = %d, want 0: a failed write is not re-read", n)
	}
	e := errorLines(stderr)
	for _, needle := range []string{"Railway", "after 3 attempts", "setting gateway.ENVIRONMENT", "(28)"} {
		if !strings.Contains(e, needle) {
			t.Errorf("stderr error lines lack %q: %q", needle, e)
		}
	}
	if got := s.sleeps(t); !slices.Equal(got, []string{"5", "10"}) {
		t.Errorf("sleeps = %v, want [5 10]", got)
	}
	if strings.Contains(stdout, productionConfirmation) {
		t.Errorf("stdout confirms a write that never landed: %q", stdout)
	}
}

func TestVerifySPADomains_RetryKeepsEveryDiscoveredURL(t *testing.T) {
	spas := []struct{ id, key string }{
		{retryLandingID, "LANDING_URL"}, {retryAppID, "APP_URL"},
		{retryOpsID, "OPS_CONSOLE_URL"}, {retrySupportID, "SUPPORT_CONSOLE_URL"},
	}
	r := map[string]string{}
	var want string
	for _, spa := range spas {
		host := spa.id + "-pr-900.up.railway.app"
		r["dom-"+retryForkEnv+"-"+spa.id] = domainsOf(false, host)
		want += spa.key + "=https://" + host + "\n"
	}
	s := newAuthShim(t, r, nil)
	writeFile(t, filepath.Join(s.dir, "probe.body"), "ok")
	writeFile(t, filepath.Join(s.dir, "probe.code"), "200")
	setFaults(t, s, "dom", "timeout", "503")
	genv := filepath.Join(t.TempDir(), "github_env")
	stdout, stderr, code := s.run(t, retryExports()+"export GITHUB_ENV='"+genv+"'\n", "verify-spa-domains", retryForkEnv)

	if code != 0 {
		t.Errorf("exit %d, want 0; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "dom"); n != len(spas)+2 {
		t.Errorf("dom calls = %d, want %d (one per SPA plus two retries)", n, len(spas)+2)
	}
	raw, err := os.ReadFile(genv)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if string(raw) != want {
		t.Errorf("GITHUB_ENV = %q, want exactly %q", raw, want)
	}
	if w := warningLines(stderr); len(w) != 1 || !strings.Contains(w[0], "attempt 3/3") {
		t.Errorf("stderr warnings = %q, want one naming attempt 3/3", w)
	}
}

// darkGatewayFixture makes verify-gateway-domain find the gateway hostname dark, so it heals.
func darkGatewayFixture(t *testing.T) authShim {
	t.Helper()
	s := reconcileForkFixture(t)
	writeFile(t, filepath.Join(s.dir, "probe.body"), `{"status":"error","code":404,"message":"Application not found"}`)
	writeFile(t, filepath.Join(s.dir, "probe.code"), "404")
	return s
}

func TestHealDomain_Delete5xxIsSentOnceAndNamed(t *testing.T) {
	s := darkGatewayFixture(t)
	writeFile(t, filepath.Join(s.dir, "domDelete.json"), `{"data":{"serviceDomainDelete":true}}`)
	setFaults(t, s, "domDelete", "503")
	stdout, stderr, code := s.run(t, retryExports(), "verify-gateway-domain", retryForkEnv)

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "domDelete"); n != 1 {
		t.Errorf("domDelete calls = %d, want 1: serviceDomainDelete is sent once", n)
	}
	if n := opCount(t, s, "domCreate"); n != 0 {
		t.Errorf("domCreate calls = %d, want 0 after a failed delete", n)
	}
	e := errorLines(stderr)
	for _, needle := range []string{"503", "deleting the unroutable gateway domain"} {
		if !strings.Contains(e, needle) {
			t.Errorf("stderr error lines lack %q: %q", needle, e)
		}
	}
}

func TestDeleteEnvironmentTimeoutKeepsTwoRounds(t *testing.T) {
	list := `{"data":{"environments":{"edges":[` +
		`{"node":{"id":"` + persistentEnvironmentID + `","name":"production","isEphemeral":false}},` +
		`{"node":{"id":"env-pr-900","name":"pr-900","isEphemeral":true}}]}}}`
	s := newAuthShim(t, map[string]string{"envList": list, "deletePrEnvironment": `{"data":{"environmentDelete":true}}`}, nil)
	setFaults(t, s, "deletePrEnvironment", "timeout", "timeout")
	stdout, stderr, code := s.run(t, forkExports(true, true, true), "delete-environment", "pr-900")

	if code != 1 {
		t.Errorf("exit %d, want 1: the environment is still listed; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "deletePrEnvironment"); n != 2 {
		t.Errorf("deletePrEnvironment calls = %d, want 2: one per round, no inner retry", n)
	}
}

func TestBucketCreateTimeoutIsSentOnce(t *testing.T) {
	s := reconcileForkFixture(t)
	// The probe sees no instance; the confirm poll finds the one Railway created anyway.
	writeFile(t, filepath.Join(s.dir, "bucketCreds-"+retryForkEnv+".seq"), gqlNotAuthorized+"\n"+bucketNamed(retryForkBucket)+"\n")
	setFaults(t, s, "bucketCreate", "timeout")
	stdout, stderr, code := runReconcileFork(t, s)

	if code != 0 {
		t.Errorf("exit %d, want 0: the confirm poll adopts the bucket instance; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "bucketCreate"); n != 1 {
		t.Errorf("bucketCreate calls = %d, want 1: bucketCreate is sent once", n)
	}
}

// postgresFirstRead answers the first svcInstance read with status, later ones from svcInstance.json.
func postgresFirstRead(t *testing.T, s authShim, status string) {
	t.Helper()
	writeFile(t, filepath.Join(s.dir, "svcInstance.seq"), postgresInstance(status)+"\n")
}

func TestPostgresDeployTimeoutIsSentOnceThenFallsBack(t *testing.T) {
	s := reconcileForkFixture(t)
	postgresFirstRead(t, s, "NONE")
	setFaults(t, s, "svcDeploy", "timeout")
	stdout, stderr, code := runReconcileFork(t, s)

	if code != 0 {
		t.Errorf("exit %d, want 0: the redeploy fallback runs; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "svcDeploy"); n != 1 {
		t.Errorf("svcDeploy calls = %d, want 1: serviceInstanceDeployV2 is sent once", n)
	}
	if n := opCount(t, s, "svcRedeploy"); n != 1 {
		t.Errorf("svcRedeploy calls = %d, want 1", n)
	}
}

func TestPostgresRedeployTimeoutIsSentOnce(t *testing.T) {
	for _, c := range []struct {
		name, status string
		faults       map[string][]string
		volumeSeq    bool
		wantRedeploy int
		wantErr      string
	}{
		{"fallback after a failed deploy", "NONE", map[string][]string{"svcDeploy": {"timeout"}, "svcRedeploy": {"timeout"}}, false, 1, "Both serviceInstanceDeployV2 and serviceInstanceRedeploy failed"},
		{"a FAILED deployment", "FAILED", map[string][]string{"svcRedeploy": {"timeout"}}, false, 1, "serviceInstanceRedeploy failed for postgres"},
		{"after creating the volume", "SUCCESS", map[string][]string{"svcRedeploy": {"timeout"}}, true, 1, "after creating its volume"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := reconcileForkFixture(t)
			postgresFirstRead(t, s, c.status)
			if c.volumeSeq {
				writeFile(t, filepath.Join(s.dir, "vols-"+retryForkEnv+".seq"), volumesOf()+"\n")
			}
			for op, f := range c.faults {
				setFaults(t, s, op, f...)
			}
			stdout, stderr, code := runReconcileFork(t, s)

			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
			}
			if n := opCount(t, s, "svcRedeploy"); n != c.wantRedeploy {
				t.Errorf("svcRedeploy calls = %d, want %d: serviceInstanceRedeploy is sent once", n, c.wantRedeploy)
			}
			e := errorLines(stdout + stderr)
			if !strings.Contains(e, c.wantErr) || !strings.Contains(e, "(28)") {
				t.Errorf("error lines lack %q and the timeout: %q", c.wantErr, e)
			}
		})
	}
}

func TestCommitStagedTimeoutIsSentOnce(t *testing.T) {
	for _, c := range []struct{ name, seqFile, seq, createOp, wantErr string }{
		{"volume", "vols-" + retryForkEnv + ".json", volumesOf(), "volCreate", "The postgres volume was created but is STAGED"},
		{"bucket", "bucketCreds-" + retryForkEnv + ".json", gqlNotAuthorized, "bucketCreate", "Any staged bucket instance stays staged"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := reconcileForkFixture(t)
			writeFile(t, filepath.Join(s.dir, c.seqFile), c.seq)
			writeFile(t, filepath.Join(s.dir, "staged.json"), `{"data":{"environment":{"id":"`+retryForkEnv+`","name":"pr-900","unmergedChangesCount":1}}}`)
			writeFile(t, filepath.Join(s.dir, "commitStaged.json"), `{"data":{"environmentPatchCommitStaged":"commit-1"}}`)
			setFaults(t, s, "commitStaged", "timeout")
			stdout, stderr, code := runReconcileFork(t, s)

			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
			}
			if n := opCount(t, s, c.createOp); n != 1 {
				t.Fatalf("control: %s calls = %d, want 1, so the staged path was never reached; output = %q", c.createOp, n, stdout+stderr)
			}
			if n := opCount(t, s, "commitStaged"); n != 1 {
				t.Errorf("commitStaged calls = %d, want 1: environmentPatchCommitStaged is sent once", n)
			}
			if e := errorLines(stdout + stderr); !strings.Contains(e, c.wantErr) {
				t.Errorf("error lines lack %q: %q", c.wantErr, e)
			}
		})
	}
}

func TestVolumeCreateTimeoutWithNoVolumeFails(t *testing.T) {
	s := reconcileForkFixture(t)
	writeFile(t, filepath.Join(s.dir, "vols-"+retryForkEnv+".json"), volumesOf())
	setFaults(t, s, "volCreate", "timeout")
	stdout, stderr, code := runReconcileFork(t, s)

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "volCreate"); n != 1 {
		t.Errorf("volCreate calls = %d, want 1", n)
	}
	e := errorLines(stdout + stderr)
	if !strings.Contains(e, "volumeCreate failed") || !strings.Contains(e, "(28)") {
		t.Errorf("error lines do not name the create's timeout: %q", e)
	}
}

// An "ok" fault falls through to normal routing: the pre-create reads answer, then the confirm ticks fault.
func TestConfirmPolls_ThirdTransientTickEndsThePoll(t *testing.T) {
	for _, c := range []struct {
		name, op, file, body, last string
		faults                     []string
		wantCalls                  int
	}{
		{"volume", "vols", "vols-" + retryForkEnv + ".json", volumesOf(), "(28)", []string{"ok", "ok", "timeout", "ok", "503", "timeout"}, 5},
		{"bucket", "bucketCreds", "bucketCreds-" + retryForkEnv + ".json", gqlNotAuthorized, "502", []string{"ok", "timeout", "ok", "timeout", "502"}, 5},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := reconcileForkFixture(t)
			writeFile(t, filepath.Join(s.dir, c.file), c.body)
			setFaults(t, s, c.op, c.faults...)
			stdout, stderr, code := runReconcileFork(t, s)

			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
			}
			if n := opCountIn(t, s, c.op, retryForkEnv); n != c.wantCalls {
				t.Errorf("fork %s calls = %d, want %d: the poll ends at its 3rd transient tick in total", c.op, n, c.wantCalls)
			}
			e := errorLines(stderr)
			for _, needle := range []string{"Railway", "3 poll ticks", "confirming", c.last} {
				if !strings.Contains(e, needle) {
					t.Errorf("stderr error lines lack %q: %q", needle, e)
				}
			}
		})
	}
}

func TestConfirmPolls_GraphQLErrorEndsThePoll(t *testing.T) {
	for _, c := range []struct {
		name, op, seqFile, seq, wantMsg string
		faults                          []string
		wantCalls                       int
	}{
		{"volume", "vols", "vols-" + retryForkEnv + ".json", volumesOf(), "Not Authorized", []string{"ok", "ok", "gqlerr"}, 2},
		{"bucket", "bucketCreds", "bucketCreds-" + retryForkEnv + ".seq", gqlNotAuthorized + "\n" + gqlProblem + "\n", "Problem processing request", nil, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := reconcileForkFixture(t)
			writeFile(t, filepath.Join(s.dir, c.seqFile), c.seq)
			if c.faults != nil {
				setFaults(t, s, c.op, c.faults...)
			}
			stdout, stderr, code := runReconcileFork(t, s)

			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
			}
			if n := opCountIn(t, s, c.op, retryForkEnv); n != c.wantCalls {
				t.Errorf("fork %s calls = %d, want %d: a GraphQL error ends the poll", c.op, n, c.wantCalls)
			}
			if e := errorLines(stderr); !strings.Contains(e, c.wantMsg) {
				t.Errorf("stderr error lines do not name %q: %q", c.wantMsg, e)
			}
		})
	}
}

func TestWaitForPostgres_TransientTicksDoNotEndThePollEarly(t *testing.T) {
	s := postgresNeverDeployedFixture(t)
	writeFile(t, filepath.Join(s.dir, "dep.seq"), `{"data":{"deployment":{"id":"dep-pg-new","status":"BUILDING"}}}`+"\n")
	writeFile(t, filepath.Join(s.dir, "dep.json"), `{"data":{"deployment":{"id":"dep-pg-new","status":"SUCCESS"}}}`)
	setFaults(t, s, "dep", "timeout", "ok", "503")
	stdout, stderr, code := runReconcileFork(t, s)

	if code != 0 {
		t.Errorf("exit %d, want 0: two transient ticks leave the poll running; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "dep"); n != 4 {
		t.Errorf("dep calls = %d, want 4 (timeout, BUILDING, 503, SUCCESS)", n)
	}
	if !strings.Contains(stdout, "postgres reached SUCCESS") {
		t.Errorf("stdout lacks the SUCCESS line; stdout = %q", stdout)
	}
}

func TestRailwayAPI_SavesTheLastRateLimitHeaders(t *testing.T) {
	s := newSealedShim(t, sourceInstances(), plainOn("PORT", sealedGatewayID))
	writeFile(t, filepath.Join(s.dir, "hdr.txt"), "HTTP/2 200\r\ncontent-type: application/json\r\n"+
		"ratelimit-policy: \"default\";q=1000;w=3600\r\nX-RateLimit-Limit: 1000\r\nx-ratelimit-remaining: 997\r\n\r\n")
	tmp := t.TempDir()
	stdout, stderr, code := s.run(t, forkExports(true, true, true)+"export RUNNER_TEMP='"+tmp+"'\n", "audit-sealed-variables")

	if code != 0 {
		t.Fatalf("exit %d, want 0; output = %q", code, stdout+stderr)
	}
	raw, err := os.ReadFile(filepath.Join(tmp, "railway-api-ratelimit"))
	if err != nil {
		t.Fatalf("the rate-limit file was not written: %v", err)
	}
	want := "ratelimit-policy: \"default\";q=1000;w=3600\nX-RateLimit-Limit: 1000\nx-ratelimit-remaining: 997\n"
	if string(raw) != want {
		t.Errorf("rate-limit file = %q, want %q", raw, want)
	}
}

// transportScript drives graphql_try alone against the curl in dir.
func transportScript(t *testing.T, dir string) string {
	t.Helper()
	return "set -euo pipefail\nexport PATH='" + dir + "':\"$PATH\"\n" +
		"RAILWAY_GRAPHQL_URL=https://example.invalid RAILWAY_API_TOKEN=tok API_COMMAND=test\n" +
		"GQL_RESPONSE='' GQL_ERROR='' GQL_FAULT='' GQL_LAST='' GQL_CURL_RC=0 GQL_REPORTED=0\n" +
		shellFunctionSource(t, "gql_errors", "gql_attempt", "graphql_try") +
		"graphql_try \"$(cat '" + filepath.Join(dir, "body") + "')\" 'sending the test body'\nprintf '%s' \"$GQL_RESPONSE\"\n"
}

// A body over the pipe buffer: bash 5 moves a large here-string to a temp file, bash 3.2 any.
func largeSecretBody() string {
	return `{"query":"mutation varUpsert { x }","variables":{"v":"` + strings.Repeat("s3cr3t-", 20000) + `"}}`
}

func TestRailwayAPI_BodyReachesCurlThroughAPipe(t *testing.T) {
	dir := t.TempDir()
	body := largeSecretBody()
	writeFile(t, filepath.Join(dir, "body"), body)
	stub := "#!/bin/sh\nif [ -p /dev/stdin ]; then echo pipe; else echo file; fi > '" + filepath.Join(dir, "stdin.kind") + "'\n" +
		"cat > '" + filepath.Join(dir, "stdin.body") + "'\necho '{\"data\":{\"ok\":true}}'\n"
	if err := os.WriteFile(filepath.Join(dir, "curl"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runBashScript(t, transportScript(t, dir))

	if code != 0 || stdout != `{"data":{"ok":true}}` {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if kind := strings.TrimSpace(readFileT(t, filepath.Join(dir, "stdin.kind"))); kind != "pipe" {
		t.Errorf("curl's stdin is a %s, want a pipe: the body touched the disk", kind)
	}
	if got := readFileT(t, filepath.Join(dir, "stdin.body")); got != body {
		t.Errorf("curl read %d bytes, want the %d-byte body intact", len(got), len(body))
	}
}

func TestRailwayAPI_CurlThatNeverReadsStdinStillSucceeds(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "body"), largeSecretBody())
	if err := os.WriteFile(filepath.Join(dir, "curl"), []byte("#!/bin/sh\necho '{\"data\":{\"ok\":true}}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runBashScript(t, transportScript(t, dir))

	if code != 0 || stdout != `{"data":{"ok":true}}` {
		t.Errorf("exit %d, stdout %q, stderr %q: an unread body must not fail the call", code, stdout, stderr)
	}
}

func readFileT(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestBucketConfirm_HTTP4xxNotAuthorizedEndsThePoll(t *testing.T) {
	s := reconcileForkFixture(t)
	// Probe: "Not Authorized" (no instance), so bucketCreate runs; the first confirm tick answers HTTP 403.
	setFaults(t, s, "bucketCreds", "gqlerr", "403")
	writeFile(t, filepath.Join(s.dir, "faultbody-bucketCreds"), gqlNotAuthorized)
	stdout, stderr, code := runReconcileFork(t, s)

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
	}
	if n := opCountIn(t, s, "bucketCreds", retryForkEnv); n != 2 {
		t.Errorf("fork bucketCreds calls = %d, want 2: an HTTP 4xx ends the poll at once", n)
	}
	if e := errorLines(stderr); !strings.Contains(e, "HTTP 403") {
		t.Errorf("stderr error lines do not name HTTP 403: %q", e)
	}
}
