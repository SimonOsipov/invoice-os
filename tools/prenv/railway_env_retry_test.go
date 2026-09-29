// railway_env_retry_test.go drives railway-env.sh's Railway transport against a faulting curl:
// what it retries, what it fails on at once, and what it prints. Guards pass at HEAD; the rest are red.
package main

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
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
	m := healthyMap()
	var edges []string
	stores := map[string]map[string]string{}
	for svc, vars := range m {
		edges = append(edges, instance("svc-"+svc, svc))
		stores["svc-"+svc] = vars
	}
	s := newAuthShim(t, map[string]string{"settle": `{"data":{"environment":{"serviceInstances":{"edges":[` + strings.Join(edges, ",") + `]}}}}`}, stores)
	setFaults(t, s, "svcVars", "503")
	stdout, stderr, code := s.run(t, forkExports(true, true, true), "assert-db-dsns", retryForkEnv)

	if code != 0 {
		t.Errorf("exit %d, want 0: an HTTP 503 on a read is retried; output = %q", code, stdout+stderr)
	}
	if !strings.Contains(stdout, "DSN check clean") {
		t.Errorf("stdout lacks the DSN report; stdout = %q", stdout)
	}
	if n := opCount(t, s, "svcVars"); n != len(m)+1 {
		t.Errorf("svcVars calls = %d, want %d (one per service plus the retry)", n, len(m)+1)
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
	s := newSealedShim(t, sourceInstances(), plainOn("PORT", sealedGatewayID))
	setFaults(t, s, "sealedAudit", "503", "503", "503")
	stdout, stderr, code := s.run(t, forkExports(true, true, true), "audit-sealed-variables")

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, stdout+stderr)
	}
	if n := opCount(t, s, "sealedAudit"); n != 3 {
		t.Errorf("sealedAudit calls = %d, want 3", n)
	}
	if e := errorLines(stdout + stderr); !strings.Contains(e, "503") {
		t.Errorf("error lines do not name 503: %q", e)
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
func TestRailwayAPI_NotAuthorizedIsNotRetried(t *testing.T) {
	s := newSealedShim(t, sourceInstances(), plainOn("PORT", sealedGatewayID))
	setFaults(t, s, "sealedAudit", "gqlerr")
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
func ensureEnvironmentFailsOnce(t *testing.T, fault string) (s authShim, errors string) {
	t.Helper()
	s = newAuthShim(t, map[string]string{"envList": envListWithout()}, nil)
	setFaults(t, s, "envList", fault, fault, fault)
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
	}
}

func TestRailwayAPI_RateLimitIsNotRetried(t *testing.T) {
	_, e := ensureEnvironmentFailsOnce(t, "429")
	if !strings.Contains(e, "429") || !regexp.MustCompile(`(?i)rate.?limit`).MatchString(e) {
		t.Errorf("error lines do not name the rate limit (HTTP 429): %q", e)
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
	s := newSealedShim(t, sourceInstances(), plainOn("PORT", sealedGatewayID))
	setFaults(t, s, "sealedAudit", "timeout")
	tmp := t.TempDir()
	stdout, stderr, code := s.run(t, forkExports(true, true, true)+"export RUNNER_TEMP='"+tmp+"'\n", "audit-sealed-variables")

	if code != 0 {
		t.Errorf("exit %d, want 0; output = %q", code, stdout+stderr)
	}
	raw, err := os.ReadFile(filepath.Join(tmp, "railway-api-calls.tsv"))
	if err != nil {
		t.Fatalf("the call log was not written: %v", err)
	}
	want := []string{"audit-sealed-variables\t1\ttransient", "audit-sealed-variables\t2\tok"}
	if got := strings.Split(strings.TrimSpace(string(raw)), "\n"); !slices.Equal(got, want) {
		t.Errorf("call log = %q, want %q", got, want)
	}
}
