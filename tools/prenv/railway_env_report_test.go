// railway_env_report_test.go drives railway-env.sh query (dev-env.yml's inline Railway calls)
// and report-api-calls (Prepare's call count) against the faulting curl of authShim.
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
)

const (
	queryCtx          = "reading serviceInstances for environment " + retryForkEnv
	queryProjectToken = "tok-project-sentinel-not-real"

	// Body shape copied from dev-env.yml's Watch Paths step (anonymous query, variables.e).
	watchPathsBody = `{"query":"query($e:String!){environment(id:$e){serviceInstances{edges{node{serviceName watchPatterns}}}}}","variables":{"e":"` + retryForkEnv + `"}}`
	watchPathsResp = `{"data":{"environment":{"serviceInstances":{"edges":[{"node":{"serviceName":"gateway","watchPatterns":[]}},{"node":{"serviceName":"auth","watchPatterns":[]}}]}}}}`

	noCallsLine = "No Railway API calls recorded." // report-api-calls, Design "The transport"
)

var (
	reportCounts    = regexp.MustCompile(`^Railway API: (\d+) calls, (\d+) attempts, (\d+) retried;`)
	reportByCommand = regexp.MustCompile(`by command: ([^;]*);`)
	reportCmdCount  = regexp.MustCompile(`([a-z][a-z-]*)=(\d+)`)
	reportRateLimit = regexp.MustCompile(`ratelimit-policy=(\S+) x-ratelimit-limit=(\S+) x-ratelimit-remaining=(\S+)\s*$`)
)

func newQueryShim(t *testing.T, resp string) authShim {
	t.Helper()
	return newAuthShim(t, map[string]string{"anonymous": resp}, nil)
}

// runQuery pipes body into `railway-env.sh query ctx`, as dev-env.yml does.
func runQuery(t *testing.T, s authShim, exports, body, ctx string) (stdout, stderr string, code int) {
	t.Helper()
	return runBashScript(t, s.prelude+exports+"printf '%s' \"$1\" | bash '"+railwayEnvScript(t)+"' query \"$2\"\n", body, ctx)
}

func sameJSON(t *testing.T, a, b string) bool {
	t.Helper()
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

func TestQuery_RetriesATimeoutAndPrintsOnlyTheBody(t *testing.T) {
	for _, fault := range []string{"timeout", "503"} {
		t.Run(fault, func(t *testing.T) {
			s := newQueryShim(t, watchPathsResp)
			setFaults(t, s, "anonymous", fault)
			stdout, stderr, code := runQuery(t, s, forkExports(true, true, false), watchPathsBody, queryCtx)

			if code != 0 {
				t.Fatalf("exit %d, want 0: query retries a %s; stdout = %q, stderr = %q", code, fault, stdout, stderr)
			}
			if stdout != watchPathsResp {
				t.Errorf("stdout = %q, want exactly the response body %q", stdout, watchPathsResp)
			}
			calls, err := os.ReadFile(s.log)
			if err != nil {
				t.Fatalf("the stub logged no call: %v", err)
			}
			lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
			if len(lines) != 2 {
				t.Fatalf("calls = %d, want 2 (the %s, then the retry)", len(lines), fault)
			}
			for i, l := range lines {
				if !sameJSON(t, l, watchPathsBody) {
					t.Errorf("call %d sent %q, want the stdin body %q", i+1, l, watchPathsBody)
				}
			}
			w := warningLines(stderr)
			if len(w) != 1 || !strings.Contains(w[0], queryCtx) || !strings.Contains(w[0], "attempt 2/3") {
				t.Errorf("stderr warnings = %q, want one naming %q and attempt 2/3", w, queryCtx)
			}
			if strings.Contains(stdout, "::") {
				t.Errorf("a diagnostic reached stdout: %q", stdout)
			}
			if got := s.sleeps(t); !slices.Equal(got, []string{"5"}) {
				t.Errorf("sleeps = %v, want [5]", got)
			}
		})
	}
}

func TestQuery_GraphQLErrorExitsWithEmptyStdout(t *testing.T) {
	s := newQueryShim(t, `{"errors":[{"message":"boom-query","extensions":{"code":"BAD_USER_INPUT"}}]}`)
	stdout, stderr, code := runQuery(t, s, forkExports(true, true, false), watchPathsBody, queryCtx)

	if code != 1 {
		t.Errorf("exit %d, want 1: a GraphQL error fails query; stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on a GraphQL error", stdout)
	}
	if n := len(s.calls(t)); n != 1 {
		t.Errorf("calls = %d, want 1: a GraphQL error is not retried", n)
	}
	if e := errorLines(stderr); !strings.Contains(e, "boom-query") || !strings.Contains(e, queryCtx) {
		t.Errorf("stderr error lines = %q, want the GraphQL message and the context", e)
	}
	if got := s.sleeps(t); len(got) != 0 {
		t.Errorf("sleeps = %v, want none", got)
	}
}

func TestQuery_ProjectTokenUsesProjectAccessTokenHeader(t *testing.T) {
	s := newQueryShim(t, watchPathsResp)
	exports := "export RAILWAY_PROJECT_TOKEN=" + queryProjectToken + "\n" + forkExports(false, true, false)
	stdout, stderr, code := runQuery(t, s, exports, watchPathsBody, queryCtx)

	if code != 0 || stdout != watchPathsResp {
		t.Fatalf("exit %d, stdout %q, want 0 and the body: the dispatch path's project token is accepted; stderr = %q", code, stdout, stderr)
	}
	argv := s.argv(t)
	if argv == "" {
		t.Fatal("curl was never called")
	}
	if !strings.Contains(argv, "Project-Access-Token: "+queryProjectToken) {
		t.Errorf("curl argv lacks the Project-Access-Token header; argv = %q", argv)
	}
	if strings.Contains(argv, "Bearer") || strings.Contains(argv, "Authorization:") {
		t.Errorf("curl argv carries an Authorization Bearer header; argv = %q", argv)
	}
}

func TestQuery_NoTokenMakesNoCall(t *testing.T) {
	s := newQueryShim(t, watchPathsResp)
	stdout, stderr, code := runQuery(t, s, forkExports(false, true, false), watchPathsBody, queryCtx)

	if n := len(s.calls(t)); n != 0 {
		t.Errorf("calls = %d, want 0 with no token", n)
	}
	s.requireLogs(t)
	if code != 1 {
		t.Errorf("exit %d, want 1 with no token; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	e := errorLines(stderr)
	for _, name := range []string{"RAILWAY_API_TOKEN", "RAILWAY_PROJECT_TOKEN"} {
		if !strings.Contains(e, name) {
			t.Errorf("stderr error lines do not name %s: %q", name, e)
		}
	}
}

// runReport runs report-api-calls with no Railway variable; runnerTemp "" leaves RUNNER_TEMP unset.
func runReport(t *testing.T, runnerTemp string) (stdout, stderr string, code int) {
	t.Helper()
	exports := ""
	if runnerTemp != "" {
		exports = "export RUNNER_TEMP='" + runnerTemp + "'\n"
	}
	return runBashScript(t, exports+"bash '"+railwayEnvScript(t)+"' report-api-calls\n")
}

// reportLine runs report-api-calls and returns its single "Railway API:" line from stdout.
func reportLine(t *testing.T, runnerTemp string) string {
	t.Helper()
	stdout, stderr, code := runReport(t, runnerTemp)
	if code != 0 {
		t.Fatalf("report-api-calls exit %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	var found []string
	for _, l := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(l, "Railway API:") {
			found = append(found, l)
		}
	}
	if len(found) != 1 {
		t.Fatalf("stdout has %d \"Railway API:\" lines, want 1; stdout = %q", len(found), stdout)
	}
	return found[0]
}

func rateLimitOf(t *testing.T, line string) []string {
	t.Helper()
	m := reportRateLimit.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("report line lacks ratelimit-policy=… x-ratelimit-limit=… x-ratelimit-remaining=…: %q", line)
	}
	return m[1:]
}

func TestReportAPICalls_CountsCallsAttemptsAndRetries(t *testing.T) {
	tmp := t.TempDir()
	exports := forkExports(true, true, true) + "export RUNNER_TEMP='" + tmp + "'\n"

	audit := newSealedShim(t, sourceInstances(), plainOn("PORT", sealedGatewayID))
	if stdout, stderr, code := audit.run(t, exports, "audit-sealed-variables"); code != 0 {
		t.Fatalf("audit-sealed-variables exit %d; output = %q", code, stdout+stderr)
	}

	m := healthyMap()
	var edges []string
	stores := map[string]map[string]string{}
	for svc, vars := range m {
		edges = append(edges, instance("svc-"+svc, svc))
		stores["svc-"+svc] = vars
	}
	dsns := newAuthShim(t, map[string]string{"settle": `{"data":{"environment":{"serviceInstances":{"edges":[` + strings.Join(edges, ",") + `]}}}}`}, stores)
	setFaults(t, dsns, "varsRead", "503")
	if stdout, stderr, code := dsns.run(t, exports, "assert-db-dsns", retryForkEnv); code != 0 {
		t.Fatalf("assert-db-dsns exit %d; output = %q", code, stdout+stderr)
	}

	auditCalls := len(audit.calls(t))
	dsnAttempts := len(dsns.calls(t))
	// assert-db-dsns: settle plus one batched varsRead; the 503 adds an attempt.
	if auditCalls != 1 || dsnAttempts != 3 {
		t.Fatalf("fixture: audit calls %d (want 1), assert-db-dsns attempts %d (want 3: settle, varsRead, varsRead)", auditCalls, dsnAttempts)
	}
	attempts := auditCalls + dsnAttempts
	calls := attempts - 1

	line := reportLine(t, tmp)
	c := reportCounts.FindStringSubmatch(line)
	if c == nil {
		t.Fatalf("report line does not start \"Railway API: <n> calls, <n> attempts, <n> retried;\": %q", line)
	}
	if got := []string{c[1], c[2], c[3]}; !slices.Equal(got, []string{strconv.Itoa(calls), strconv.Itoa(attempts), "1"}) {
		t.Errorf("calls, attempts, retried = %v, want [%d %d 1]; line = %q", got, calls, attempts, line)
	}

	seg := reportByCommand.FindStringSubmatch(line)
	if seg == nil {
		t.Fatalf("report line lacks \"by command: …;\": %q", line)
	}
	by := map[string]int{}
	for _, kv := range reportCmdCount.FindAllStringSubmatch(seg[1], -1) {
		n, _ := strconv.Atoi(kv[2])
		by[kv[1]] = n
	}
	if len(by) == 0 {
		t.Fatalf("the by-command breakdown is empty: %q", seg[1])
	}
	want := map[string]int{"audit-sealed-variables": 1, "assert-db-dsns": 2}
	if !reflect.DeepEqual(by, want) {
		t.Errorf("by command = %v, want %v", by, want)
	}
	if got := rateLimitOf(t, line); !slices.Equal(got, []string{"n/a", "n/a", "n/a"}) {
		t.Errorf("rate limit = %v, want n/a for each header Railway did not send", got)
	}
}

// reportAfterHeaders sends one call whose response carries hdr, then reports.
func reportAfterHeaders(t *testing.T, hdr string) string {
	t.Helper()
	s := newSealedShim(t, sourceInstances(), plainOn("PORT", sealedGatewayID))
	writeFile(t, filepath.Join(s.dir, "hdr.txt"), "HTTP/2 200\r\ncontent-type: application/json\r\n"+hdr+"\r\n")
	tmp := t.TempDir()
	if stdout, stderr, code := s.run(t, forkExports(true, true, true)+"export RUNNER_TEMP='"+tmp+"'\n", "audit-sealed-variables"); code != 0 {
		t.Fatalf("audit-sealed-variables exit %d; output = %q", code, stdout+stderr)
	}
	return reportLine(t, tmp)
}

func TestReportAPICalls_PrintsLowercaseRateLimitHeaders(t *testing.T) {
	for _, c := range []struct{ name, hdr string }{
		// Backboard answers lowercase HTTP/2 names (story INFRA-02, [call-count]).
		{"lowercase", "ratelimit-policy: \"default\";q=10000;w=3600\r\nx-ratelimit-limit: 10000\r\nx-ratelimit-remaining: 9876\r\n"},
		{"mixed case", "RateLimit-Policy: \"default\";q=10000;w=3600\r\nX-RateLimit-Limit: 10000\r\nX-RateLimit-Remaining: 9876\r\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			line := reportAfterHeaders(t, c.hdr)
			if got := rateLimitOf(t, line); !slices.Equal(got, []string{`"default";q=10000;w=3600`, "10000", "9876"}) {
				t.Errorf("rate limit = %v, want the three header values; line = %q", got, line)
			}
		})
	}
}

func TestReportAPICalls_PolicyOnlyHeaders(t *testing.T) {
	line := reportAfterHeaders(t, "ratelimit-policy: \"default\";q=1000;w=3600\r\n")
	if got := rateLimitOf(t, line); !slices.Equal(got, []string{`"default";q=1000;w=3600`, "n/a", "n/a"}) {
		t.Errorf("rate limit = %v, want the policy and n/a for the absent limit and remaining; line = %q", got, line)
	}
}

func TestReportAPICalls_NoLogSaysSo(t *testing.T) {
	for _, c := range []struct{ name, runnerTemp string }{
		{"empty RUNNER_TEMP directory", t.TempDir()},
		{"RUNNER_TEMP unset", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			stdout, stderr, code := runReport(t, c.runnerTemp)
			if code != 0 {
				t.Errorf("exit %d, want 0: the report never fails the job; stdout = %q, stderr = %q", code, stdout, stderr)
			}
			if !strings.Contains(stdout, noCallsLine) {
				t.Errorf("stdout = %q, want %q", stdout, noCallsLine)
			}
			if strings.Contains(stdout, "Railway API:") {
				t.Errorf("stdout reports counts with no log: %q", stdout)
			}
		})
	}
}

// QA adversarial coverage.

func TestQuery_RefusesBeforeAnyCall(t *testing.T) {
	projectOnly := "export RAILWAY_PROJECT_TOKEN=" + queryProjectToken + "\n"
	for _, c := range []struct {
		name, exports, script string
		code                  int
		names                 string
	}{
		{"no RAILWAY_PROJECT_ID, API token", forkExports(true, false, false), `query "$2"`, 1, "RAILWAY_PROJECT_ID"},
		{"no RAILWAY_PROJECT_ID, project token", projectOnly, `query "$2"`, 1, "RAILWAY_PROJECT_ID"},
		{"empty context", forkExports(true, true, false), `query ""`, 2, "query <context>"},
		{"no context argument", forkExports(true, true, false), `query`, 2, "query <context>"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newQueryShim(t, watchPathsResp)
			stdout, stderr, code := runBashScript(t, s.prelude+c.exports+"printf '%s' \"$1\" | bash '"+railwayEnvScript(t)+"' "+c.script+"\n", watchPathsBody, queryCtx)

			if n := len(s.calls(t)); n != 0 {
				t.Errorf("calls = %d, want 0", n)
			}
			s.requireLogs(t)
			if code != c.code {
				t.Errorf("exit %d, want %d; stderr = %q", code, c.code, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if e := errorLines(stderr); !strings.Contains(e, c.names) {
				t.Errorf("stderr error lines = %q, want them to name %q", e, c.names)
			}
		})
	}
}

func TestQuery_BothTokensSendBearer(t *testing.T) {
	s := newQueryShim(t, watchPathsResp)
	exports := "export RAILWAY_PROJECT_TOKEN=" + queryProjectToken + "\n" + forkExports(true, true, false)
	stdout, stderr, code := runQuery(t, s, exports, watchPathsBody, queryCtx)

	if code != 0 || stdout != watchPathsResp {
		t.Fatalf("exit %d, stdout %q, want 0 and the body; stderr = %q", code, stdout, stderr)
	}
	argv := s.argv(t)
	if !strings.Contains(argv, "Authorization: Bearer "+forkToken) {
		t.Errorf("curl argv lacks the Bearer header; argv = %q", argv)
	}
	if strings.Contains(argv, "Project-Access-Token") || strings.Contains(argv, queryProjectToken) {
		t.Errorf("curl argv carries the project token; argv = %q", argv)
	}
}

// A GQL_AUTH_HEADER in the caller's environment must not replace any subcommand's header.
func TestRailwayAPI_CallerEnvironmentCannotSetTheAuthHeader(t *testing.T) {
	const planted = "Project-Access-Token: planted-by-caller"
	for _, c := range []struct{ name, sub string }{
		{"query", "query"},
		{"audit-sealed-variables", "audit-sealed-variables"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var s authShim
			var code int
			var stderr string
			exports := "export GQL_AUTH_HEADER='" + planted + "'\n"
			if c.sub == "query" {
				s = newQueryShim(t, watchPathsResp)
				_, stderr, code = runQuery(t, s, exports+forkExports(true, true, false), watchPathsBody, queryCtx)
			} else {
				s = newSealedShim(t, sourceInstances(), plainOn("PORT", sealedGatewayID))
				_, stderr, code = s.run(t, exports+forkExports(true, true, true), c.sub)
			}
			if code != 0 {
				t.Fatalf("%s exit %d; stderr = %q", c.sub, code, stderr)
			}
			argv := s.argv(t)
			if !strings.Contains(argv, "Authorization: Bearer "+forkToken) {
				t.Errorf("curl argv lacks the Bearer header; argv = %q", argv)
			}
			if strings.Contains(argv, "planted-by-caller") {
				t.Errorf("the caller's GQL_AUTH_HEADER reached curl; argv = %q", argv)
			}
		})
	}
}

func TestQuery_ExhaustedBudgetEmptyStdoutExit1(t *testing.T) {
	s := newQueryShim(t, watchPathsResp)
	setFaults(t, s, "anonymous", "timeout", "timeout", "timeout")
	tmp := t.TempDir()
	stdout, stderr, code := runQuery(t, s, forkExports(true, true, false)+"export RUNNER_TEMP='"+tmp+"'\n", watchPathsBody, queryCtx)

	if code != 1 {
		t.Errorf("exit %d, want 1 after 3 timeouts; stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if n := len(s.calls(t)); n != 3 {
		t.Errorf("calls = %d, want 3", n)
	}
	if got := s.sleeps(t); !slices.Equal(got, []string{"5", "10"}) {
		t.Errorf("sleeps = %v, want [5 10]", got)
	}
	e := strings.Split(errorLines(stderr), "\n")
	if len(e) != 1 || !strings.Contains(e[0], "after 3 attempts") || !strings.Contains(e[0], queryCtx) || !strings.Contains(e[0], "(28)") {
		t.Errorf("stderr error lines = %q, want one naming 3 attempts, %q and curl's (28)", e, queryCtx)
	}

	// The retried call that ultimately failed: 1 call, 3 attempts, 1 retried.
	line := reportLine(t, tmp)
	c := reportCounts.FindStringSubmatch(line)
	if c == nil {
		t.Fatalf("report line lacks the counts: %q", line)
	}
	if got := []string{c[1], c[2], c[3]}; !slices.Equal(got, []string{"1", "3", "1"}) {
		t.Errorf("calls, attempts, retried = %v, want [1 3 1]; line = %q", got, line)
	}
	if seg := reportByCommand.FindStringSubmatch(line); seg == nil || strings.TrimSpace(seg[1]) != "query=1" {
		t.Errorf("by command = %v, want query=1; line = %q", seg, line)
	}
}

func TestReportAPICalls_MalformedLogStillExitsZero(t *testing.T) {
	t.Run("garbage rows", func(t *testing.T) {
		tmp := t.TempDir()
		writeFile(t, filepath.Join(tmp, "railway-api-calls.tsv"),
			"set-fork-auth\t1\ttransient\nset-fork-auth\t2\tok\n\ntrunc\nbad\tx\tok\nquery\t1\tok\n")
		line := reportLine(t, tmp)
		c := reportCounts.FindStringSubmatch(line)
		if c == nil {
			t.Fatalf("report line lacks the counts: %q", line)
		}
		if c[1] != "2" || c[3] != "1" {
			t.Errorf("calls, retried = %s, %s, want 2, 1: a malformed row is no call; line = %q", c[1], c[3], line)
		}
		seg := reportByCommand.FindStringSubmatch(line)
		if seg == nil || strings.TrimSpace(seg[1]) == "" {
			t.Fatalf("report line lacks a non-empty by-command breakdown: %q", line)
		}
		by := reportCmdCount.FindAllStringSubmatch(seg[1], -1)
		got := map[string]string{}
		for _, kv := range by {
			got[kv[1]] = kv[2]
		}
		if want := map[string]string{"set-fork-auth": "1", "query": "1"}; !reflect.DeepEqual(got, want) {
			t.Errorf("by command = %v, want %v; line = %q", got, want, line)
		}
	})
	t.Run("unreadable log", func(t *testing.T) {
		tmp := t.TempDir()
		log := filepath.Join(tmp, "railway-api-calls.tsv")
		writeFile(t, log, "query\t1\tok\n")
		if err := os.Chmod(log, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(log, 0o644) })
		if f, err := os.Open(log); err == nil {
			f.Close()
			t.Skip("running as a user that reads a mode-000 file")
		}
		stdout, stderr, code := runReport(t, tmp)
		if code != 0 {
			t.Errorf("exit %d, want 0: the report never fails the job; stdout = %q, stderr = %q", code, stdout, stderr)
		}
	})
}

func TestReportAPICalls_CountsARateLimitRetryAsOneCall(t *testing.T) {
	tmp := t.TempDir()
	s := newSealedShim(t, sourceInstances(), plainOn("PORT", sealedGatewayID))
	setFaults(t, s, "sealedAudit", "429")
	plantRateLimitHeaders(t, s, "retry-after: 1")
	if stdout, stderr, code := s.run(t, forkExports(true, true, true)+"export RUNNER_TEMP='"+tmp+"'\n", "audit-sealed-variables"); code != 0 {
		t.Errorf("audit-sealed-variables exit %d, want 0 after the 429 wait; output = %q", code, stdout+stderr)
	}

	line := reportLine(t, tmp)
	c := reportCounts.FindStringSubmatch(line)
	if c == nil {
		t.Fatalf("report line does not start \"Railway API: <n> calls, <n> attempts, <n> retried;\": %q", line)
	}
	if got := []string{c[1], c[2], c[3]}; !slices.Equal(got, []string{"1", "2", "1"}) {
		t.Errorf("calls, attempts, retried = %v, want [1 2 1]; line = %q", got, line)
	}
}
