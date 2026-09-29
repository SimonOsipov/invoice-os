// railway_env_wait_deployment_test.go drives railway-env.sh wait-deployment, fleet-gate's
// auth verdict, against the faulting curl of authShim.
package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	waitDepLabel = "auth"
	waitDepToken = "tok-project-wait-not-real"
	// dep.seq feeds one body per call; dep.json answers once the sequence is empty.
	waitDepUsage = "usage: railway-env.sh <assert-project-settings"
)

// Status values: story INFRA-02 [wait-deployment] (terminal and passing), and the in-flight set in
// railway-env.sh ensure_postgres_running, which also names NEEDS_APPROVAL. The live Railway
// DeploymentStatus schema was not fetched.
var (
	waitDepTerminal = []string{"FAILED", "CRASHED", "REMOVED", "REMOVING", "SKIPPED"}
	waitDepInFlight = []string{"BUILDING", "DEPLOYING", "INITIALIZING", "QUEUED", "WAITING", "NEEDS_APPROVAL"}
)

// depUnrouted ends a run that makes a call the test did not script.
const depUnrouted = `{"errors":[{"message":"unscripted dep call"}]}`

func depStatus(id, status string) string {
	return `{"data":{"deployment":{"id":"` + id + `","status":"` + status + `"}}}`
}

// newDepShim answers every dep call with fallback once dep.seq runs out.
func newDepShim(t *testing.T, fallback string, seq ...string) authShim {
	t.Helper()
	s := newAuthShim(t, map[string]string{"dep": fallback}, nil)
	if len(seq) > 0 {
		writeFile(t, filepath.Join(s.dir, "dep.seq"), strings.Join(seq, "\n")+"\n")
	}
	return s
}

func waitDepExports() string { return forkExports(true, true, false) }

// runWaitDeployment runs `wait-deployment auth <id>`. An unknown subcommand is a setup failure, not a result.
func runWaitDeployment(t *testing.T, s authShim, exports, id string) (stdout, stderr string, code int) {
	t.Helper()
	stdout, stderr, code = s.run(t, exports, "wait-deployment", waitDepLabel, id)
	if code == 2 && strings.Contains(stdout+stderr, waitDepUsage) {
		t.Fatal("railway-env.sh has no wait-deployment subcommand: it printed its top-level usage line")
	}
	return stdout, stderr, code
}

func allSleeps(t *testing.T, s authShim, want string) {
	t.Helper()
	for _, got := range s.sleeps(t) {
		if got != want {
			t.Errorf("sleeps = %v, want every sleep to be %q", s.sleeps(t), want)
			return
		}
	}
}

func TestWaitDeployment_SuccessAfterDeploying(t *testing.T) {
	s := newDepShim(t, depUnrouted, depStatus(upIDA, "BUILDING"), depStatus(upIDA, "DEPLOYING"), depStatus(upIDA, "SUCCESS"))
	stdout, stderr, code := runWaitDeployment(t, s, waitDepExports(), upIDA)

	if code != 0 {
		t.Errorf("exit %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if n := opCount(t, s, "dep"); n != 3 {
		t.Errorf("dep calls = %d, want 3 (BUILDING, DEPLOYING, SUCCESS)", n)
	}
	if got := strings.Join(s.sleeps(t), " "); got != "10 10" {
		t.Errorf("sleeps = %q, want %q", got, "10 10")
	}
	if want := "auth deployment " + upIDA + " is SUCCESS."; !strings.Contains(stdout, want) {
		t.Errorf("stdout lacks %q; stdout = %q", want, stdout)
	}
}

// AC 1: every other non-terminal status keeps the poll going.
func TestWaitDeployment_PollsThroughEveryOtherStatus(t *testing.T) {
	seq := []string{}
	for _, st := range waitDepInFlight {
		seq = append(seq, depStatus(upIDA, st))
	}
	seq = append(seq, depStatus(upIDA, "SUCCESS"))
	s := newDepShim(t, depUnrouted, seq...)
	stdout, stderr, code := runWaitDeployment(t, s, waitDepExports(), upIDA)

	if code != 0 {
		t.Errorf("exit %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if n, want := opCount(t, s, "dep"), len(waitDepInFlight)+1; n != want {
		t.Errorf("dep calls = %d, want %d: one per status", n, want)
	}
	if got := s.sleeps(t); len(got) != len(waitDepInFlight) {
		t.Errorf("sleeps = %v, want %d, one between ticks", got, len(waitDepInFlight))
	}
	allSleeps(t, s, "10")
}

func TestWaitDeployment_SleepingPasses(t *testing.T) {
	s := newDepShim(t, depStatus(upIDA, "SLEEPING"))
	stdout, stderr, code := runWaitDeployment(t, s, waitDepExports(), upIDA)

	if code != 0 {
		t.Errorf("exit %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if n := opCount(t, s, "dep"); n != 1 {
		t.Errorf("dep calls = %d, want 1", n)
	}
	if want := "auth deployment " + upIDA + " is SLEEPING."; !strings.Contains(stdout, want) {
		t.Errorf("stdout lacks %q; stdout = %q", want, stdout)
	}
}

func TestWaitDeployment_TerminalStatusFails(t *testing.T) {
	if len(waitDepTerminal) == 0 {
		t.Fatal("no terminal statuses to run")
	}
	for _, status := range waitDepTerminal {
		t.Run(status, func(t *testing.T) {
			s := newDepShim(t, depStatus(upIDA, status))
			stdout, stderr, code := runWaitDeployment(t, s, waitDepExports(), upIDA)

			if code != 1 {
				t.Errorf("exit %d, want 1; stdout = %q, stderr = %q", code, stdout, stderr)
			}
			if n := opCount(t, s, "dep"); n != 1 {
				t.Errorf("dep calls = %d, want 1: a terminal status ends the wait", n)
			}
			e := errorLines(stdout + stderr)
			for _, needle := range []string{waitDepLabel, upIDA, status} {
				if !strings.Contains(e, needle) {
					t.Errorf("error lines lack %q: %q", needle, e)
				}
			}
		})
	}
}

func TestWaitDeployment_WindowEndsNamingTheLastStatus(t *testing.T) {
	s := newDepShim(t, depStatus(upIDA, "DEPLOYING"))
	stdout, stderr, code := runWaitDeployment(t, s, waitDepExports(), upIDA)

	if code != 1 {
		t.Errorf("exit %d, want 1; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if n := opCount(t, s, "dep"); n != 60 {
		t.Errorf("dep calls = %d, want exactly 60 (600 s at 10 s)", n)
	}
	allSleeps(t, s, "10")
	e := errorLines(stdout + stderr)
	for _, needle := range []string{waitDepLabel, upIDA, "DEPLOYING"} {
		if !strings.Contains(e, needle) {
			t.Errorf("error lines lack %q: %q", needle, e)
		}
	}
}

func TestWaitDeployment_GraphQLErrorEndsAtOnce(t *testing.T) {
	s := newDepShim(t, `{"errors":[{"message":"boom-dep-lookup"}]}`)
	stdout, stderr, code := runWaitDeployment(t, s, waitDepExports(), upIDA)

	if code != 1 {
		t.Errorf("exit %d, want 1; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if n := opCount(t, s, "dep"); n != 1 {
		t.Errorf("dep calls = %d, want 1: a GraphQL error is not polled past", n)
	}
	if e := errorLines(stdout + stderr); !strings.Contains(e, "boom-dep-lookup") {
		t.Errorf("error lines do not name the GraphQL message: %q", e)
	}
}

func TestWaitDeployment_TransientTickKeepsPolling(t *testing.T) {
	s := newDepShim(t, depStatus(upIDA, "SUCCESS"))
	setFaults(t, s, "dep", "timeout")
	stdout, stderr, code := runWaitDeployment(t, s, waitDepExports(), upIDA)

	if code != 0 {
		t.Errorf("exit %d, want 0: one lost tick is polled past; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if n := opCount(t, s, "dep"); n != 2 {
		t.Errorf("dep calls = %d, want 2 (timeout, SUCCESS)", n)
	}
	// One 10 s tick wait and no transport backoff: a tick is sent once.
	if got := strings.Join(s.sleeps(t), " "); got != "10" {
		t.Errorf("sleeps = %q, want %q", got, "10")
	}
}

func TestWaitDeployment_ThirdTransientTickEnds(t *testing.T) {
	s := newDepShim(t, depStatus(upIDA, "SUCCESS"))
	setFaults(t, s, "dep", "timeout", "timeout", "timeout")
	stdout, stderr, code := runWaitDeployment(t, s, waitDepExports(), upIDA)

	if code != 1 {
		t.Errorf("exit %d, want 1; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if n := opCount(t, s, "dep"); n != 3 {
		t.Errorf("dep calls = %d, want exactly 3: the wait ends at its 3rd transient tick", n)
	}
	allSleeps(t, s, "10")
	e := errorLines(stdout + stderr)
	for _, needle := range []string{"Railway", "(28)"} {
		if !strings.Contains(e, needle) {
			t.Errorf("error lines lack %q: %q", needle, e)
		}
	}
}

// AC 4. The id comes from `railway up` output: untrusted, never a request body.
func TestWaitDeployment_EmptyOrMalformedIDMakesNoCall(t *testing.T) {
	const missing = `(?i)empty|missing|not set`
	const malformed = `(?i)uuid|malformed|invalid|not a valid`
	cases := []struct{ name, id, problem string }{
		{"empty", "", missing},
		{"not a uuid", "not-a-uuid", malformed},
		{"shell metacharacters", "70f187fe;true", malformed},
		{"a uuid with a suffix", upIDA + "-extra", malformed},
		{"a uuid with a leading space", " " + upIDA, malformed},
		{"a body-breaking quote", upIDA + `","x":"`, malformed},
		{"a workflow command on a second line", "bad\n::error::injected", malformed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newDepShim(t, depStatus(upIDA, "SUCCESS"))
			stdout, stderr, code := runWaitDeployment(t, s, waitDepExports(), c.id)

			if n := len(s.calls(t)); n != 0 {
				t.Errorf("calls = %d, want 0: the id is checked before any request", n)
			}
			s.requireLogs(t)
			if code != 1 {
				t.Errorf("exit %d, want 1; stdout = %q, stderr = %q", code, stdout, stderr)
			}
			e := errorLines(stdout + stderr)
			if !strings.Contains(e, waitDepLabel) {
				t.Errorf("error lines do not name the label %q: %q", waitDepLabel, e)
			}
			if !regexp.MustCompile(c.problem).MatchString(e) {
				t.Errorf("error lines do not name the id problem (%s): %q", c.problem, e)
			}
			for _, l := range strings.Split(stdout+stderr, "\n") {
				if strings.HasPrefix(l, "::error::injected") {
					t.Errorf("the id started a workflow command of its own: %q", l)
				}
			}
			if strings.Contains(s.argv(t), c.id) && c.id != "" {
				t.Errorf("the id reached curl's argv: %q", s.argv(t))
			}
		})
	}
}

// AC 4 in the workflow's shape: a stream failure with no Build Logs URL writes no id, so the
// fleet-gate step runs `wait-deployment auth "$AUTH_DEPLOYMENT_ID"` with it empty and must go red.
// A stream failure that did print a URL hands over an id the wait accepts.
func TestWaitDeployment_IDFromRailwayUpOutput(t *testing.T) {
	const streamFailed = "Indexing...\nUploading...\nFailed to stream build logs: Failed to retrieve build log\n"
	cases := []struct {
		name     string
		up       upAttempt
		wantCode int
		wantID   string
	}{
		{"stream failure without a Build Logs URL", upAttempt{streamFailed, 1}, 1, ""},
		{"stream failure with a Build Logs URL", upAttempt{upStreamFailed(upIDA), 1}, 0, upIDA},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			up := newUpStub(t, c.up)
			if out, code := up.run(t, waitDepLabel); code != 0 {
				t.Fatalf("control: railway-up-ci.sh exit %d, want 0 (stream failure is tolerated); output = %q", code, out)
			}
			raw, err := os.ReadFile(up.outputPath())
			if err != nil {
				t.Fatal(err)
			}
			id := ""
			for _, l := range strings.Split(string(raw), "\n") {
				if v, ok := strings.CutPrefix(l, "deployment_id_auth="); ok {
					id = v
				}
			}
			if id != c.wantID {
				t.Fatalf("control: GITHUB_OUTPUT gives id %q, want %q; file = %q", id, c.wantID, raw)
			}

			s := newDepShim(t, depStatus(upIDA, "SUCCESS"))
			exports := waitDepExports() + "export AUTH_DEPLOYMENT_ID='" + id + "'\n"
			stdout, stderr, code := runBashScript(t, s.prelude+exports+"bash '"+railwayEnvScript(t)+"' wait-deployment auth \"$AUTH_DEPLOYMENT_ID\"\n")
			if code == 2 && strings.Contains(stdout+stderr, waitDepUsage) {
				t.Fatal("railway-env.sh has no wait-deployment subcommand: it printed its top-level usage line")
			}

			if code != c.wantCode {
				t.Errorf("exit %d, want %d; stdout = %q, stderr = %q", code, c.wantCode, stdout, stderr)
			}
			if c.wantID == "" {
				if n := len(s.calls(t)); n != 0 {
					t.Errorf("calls = %d, want 0 with no id", n)
				}
				s.requireLogs(t)
				if e := errorLines(stdout + stderr); !strings.Contains(e, waitDepLabel) || !regexp.MustCompile(`(?i)empty|missing|not set`).MatchString(e) {
					t.Errorf("error lines do not name the missing auth deployment id: %q", e)
				}
				return
			}
			if n := opCount(t, s, "dep"); n != 1 {
				t.Errorf("dep calls = %d, want 1", n)
			}
			if want := "auth deployment " + upIDA + " is SUCCESS."; !strings.Contains(stdout, want) {
				t.Errorf("stdout lacks %q; stdout = %q", want, stdout)
			}
		})
	}
}

func TestWaitDeployment_SendsOnlyTheStatusQuery(t *testing.T) {
	s := newDepShim(t, depUnrouted, depStatus(upIDA, "BUILDING"), depStatus(upIDA, "SUCCESS"))
	stdout, stderr, code := runWaitDeployment(t, s, waitDepExports(), upIDA)

	if code != 0 {
		t.Fatalf("exit %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	calls := s.calls(t)
	if len(calls) == 0 {
		t.Fatal("the shim logged no call")
	}
	for i, c := range calls {
		if m := gqlOperation.FindStringSubmatch(c.Query); m == nil || m[1] != "dep" {
			t.Errorf("call %d is not the dep query: %q", i, c.Query)
		}
		if strings.Contains(c.Query, "mutation") {
			t.Errorf("call %d carries a mutation: %q", i, c.Query)
		}
		if c.Variables["id"] != upIDA {
			t.Errorf("call %d variables.id = %v, want %q", i, c.Variables["id"], upIDA)
		}
	}
	if argv := s.argv(t); !strings.Contains(argv, "Authorization: Bearer "+forkToken) || strings.Contains(argv, "Project-Access-Token") {
		t.Errorf("curl argv does not carry the account token as Bearer only; argv = %q", argv)
	}
}

func TestWaitDeployment_ProjectTokenUsesProjectAccessTokenHeader(t *testing.T) {
	s := newDepShim(t, depStatus(upIDA, "SUCCESS"))
	exports := "export RAILWAY_PROJECT_TOKEN=" + waitDepToken + "\n" + forkExports(false, true, false)
	stdout, stderr, code := runWaitDeployment(t, s, exports, upIDA)

	if code != 0 {
		t.Fatalf("exit %d, want 0: the dispatch path's project token is accepted; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	argv := s.argv(t)
	if argv == "" {
		t.Fatal("curl was never called")
	}
	if !strings.Contains(argv, "Project-Access-Token: "+waitDepToken) {
		t.Errorf("curl argv lacks the Project-Access-Token header; argv = %q", argv)
	}
	if strings.Contains(argv, "Bearer") || strings.Contains(argv, "Authorization:") {
		t.Errorf("curl argv carries an Authorization header; argv = %q", argv)
	}
}

// Both tokens: the account token wins, as in `query`.
func TestWaitDeployment_APITokenWinsOverProjectToken(t *testing.T) {
	s := newDepShim(t, depStatus(upIDA, "SUCCESS"))
	exports := "export RAILWAY_PROJECT_TOKEN=" + waitDepToken + "\n" + forkExports(true, true, false)
	stdout, stderr, code := runWaitDeployment(t, s, exports, upIDA)

	if code != 0 {
		t.Fatalf("exit %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	argv := s.argv(t)
	if !strings.Contains(argv, "Authorization: Bearer "+forkToken) {
		t.Errorf("curl argv lacks the Bearer header; argv = %q", argv)
	}
	if strings.Contains(argv, "Project-Access-Token") || strings.Contains(argv, waitDepToken) {
		t.Errorf("curl argv carries the project token; argv = %q", argv)
	}
}

func TestWaitDeployment_NoTokenMakesNoCall(t *testing.T) {
	s := newDepShim(t, depStatus(upIDA, "SUCCESS"))
	stdout, stderr, code := runWaitDeployment(t, s, forkExports(false, true, false), upIDA)

	if n := len(s.calls(t)); n != 0 {
		t.Errorf("calls = %d, want 0 with no token", n)
	}
	s.requireLogs(t)
	if code != 1 {
		t.Errorf("exit %d, want 1 with no token; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	e := errorLines(stdout + stderr)
	for _, name := range []string{"RAILWAY_API_TOKEN", "RAILWAY_PROJECT_TOKEN"} {
		if !strings.Contains(e, name) {
			t.Errorf("error lines do not name %s: %q", name, e)
		}
	}
}
