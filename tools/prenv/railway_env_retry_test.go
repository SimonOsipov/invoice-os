// railway_env_retry_test.go drives railway-env.sh's Railway transport against a faulting curl:
// what it retries, what it fails on at once, and what it prints.
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
	const cmd = "audit-sealed-variables"
	for _, c := range []struct {
		name   string
		faults []string
		code   int
		want   []string
	}{
		{"transient then ok", []string{"timeout"}, 0, []string{cmd + "\t1\ttransient", cmd + "\t2\tok"}},
		{"exhausted", []string{"503", "timeout", "503"}, 1, []string{cmd + "\t1\ttransient", cmd + "\t2\ttransient", cmd + "\t3\ttransient"}},
		{"fatal", []string{"gqlerr"}, 1, []string{cmd + "\t1\tfatal"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newSealedShim(t, sourceInstances(), plainOn("PORT", sealedGatewayID))
			setFaults(t, s, "sealedAudit", c.faults...)
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
