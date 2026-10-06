// railway_env_wait_deployment_test.go drives railway-env.sh wait-deployment, fleet-gate's
// auth verdict, against the faulting curl of authShim.
package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
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

// A status outside the known set is not terminal: the wait polls on and names it at the window's end.
func TestWaitDeployment_UnknownStatusKeepsPolling(t *testing.T) {
	const odd = "FUTURE_STATE"
	t.Run("then success", func(t *testing.T) {
		s := newDepShim(t, depUnrouted, depStatus(upIDA, odd), depStatus(upIDA, odd), depStatus(upIDA, "SUCCESS"))
		stdout, stderr, code := runWaitDeployment(t, s, waitDepExports(), upIDA)
		if code != 0 {
			t.Errorf("exit %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
		}
		if n := opCount(t, s, "dep"); n != 3 {
			t.Errorf("dep calls = %d, want 3", n)
		}
	})
	t.Run("never resolves", func(t *testing.T) {
		s := newDepShim(t, depStatus(upIDA, odd))
		stdout, stderr, code := runWaitDeployment(t, s, waitDepExports(), upIDA)
		if code != 1 {
			t.Errorf("exit %d, want 1; stdout = %q, stderr = %q", code, stdout, stderr)
		}
		if n := opCount(t, s, "dep"); n != 60 {
			t.Errorf("dep calls = %d, want 60", n)
		}
		if e := errorLines(stdout + stderr); !strings.Contains(e, odd) {
			t.Errorf("error lines do not name the last status %q: %q", odd, e)
		}
	})
}

// The 3rd transient tick in total ends the wait, not the 3rd in a row.
func TestWaitDeployment_TransientTicksCountInTotal(t *testing.T) {
	s := newDepShim(t, depStatus(upIDA, "SUCCESS"), depStatus(upIDA, "BUILDING"), depStatus(upIDA, "BUILDING"))
	// "ok" is not a fault: the shim falls through to normal routing for that call.
	setFaults(t, s, "dep", "timeout", "ok", "timeout", "ok", "timeout")
	stdout, stderr, code := runWaitDeployment(t, s, waitDepExports(), upIDA)

	if code != 1 {
		t.Errorf("exit %d, want 1; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if n := opCount(t, s, "dep"); n != 5 {
		t.Errorf("dep calls = %d, want 5 (timeout, BUILDING, timeout, BUILDING, timeout)", n)
	}
	if e := errorLines(stdout + stderr); !strings.Contains(e, "Railway") || !strings.Contains(e, "(28)") {
		t.Errorf("error lines do not name Railway and the curl fault: %q", e)
	}
}

func TestWaitDeployment_GraphQLErrorMidPollEndsAtOnce(t *testing.T) {
	s := newDepShim(t, depUnrouted, depStatus(upIDA, "BUILDING"), `{"errors":[{"message":"boom-mid-poll"}]}`, depStatus(upIDA, "SUCCESS"))
	stdout, stderr, code := runWaitDeployment(t, s, waitDepExports(), upIDA)

	if code != 1 {
		t.Errorf("exit %d, want 1; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if n := opCount(t, s, "dep"); n != 2 {
		t.Errorf("dep calls = %d, want 2: the error ends the wait, the SUCCESS behind it is never read", n)
	}
	if e := errorLines(stdout + stderr); !strings.Contains(e, "boom-mid-poll") {
		t.Errorf("error lines do not name the GraphQL message: %q", e)
	}
}

func TestWaitDeployment_UUIDShape(t *testing.T) {
	ok := []struct{ name, id string }{
		{"lowercase", upIDA},
		{"uppercase", strings.ToUpper(upIDA)},
	}
	for _, c := range ok {
		t.Run("accepts "+c.name, func(t *testing.T) {
			s := newDepShim(t, depStatus(c.id, "SUCCESS"))
			stdout, stderr, code := runWaitDeployment(t, s, waitDepExports(), c.id)
			if code != 0 {
				t.Errorf("exit %d, want 0; stdout = %q, stderr = %q", code, stdout, stderr)
			}
			calls := s.calls(t)
			if len(calls) != 1 || calls[0].Variables["id"] != c.id {
				t.Errorf("calls = %+v, want one dep call carrying id %q", calls, c.id)
			}
		})
	}
	bad := []struct{ name, id string }{
		{"last group one short", upIDA[:len(upIDA)-1]},
		{"last group one long", upIDA + "0"},
		{"first group one short", upIDA[1:]},
		{"non-hex digit", "g" + upIDA[1:]},
		{"no hyphens", strings.ReplaceAll(upIDA, "-", "")},
		{"braced", "{" + upIDA + "}"},
	}
	for _, c := range bad {
		t.Run("rejects "+c.name, func(t *testing.T) {
			s := newDepShim(t, depStatus(upIDA, "SUCCESS"))
			stdout, stderr, code := runWaitDeployment(t, s, waitDepExports(), c.id)
			if n := len(s.calls(t)); n != 0 {
				t.Errorf("calls = %d, want 0", n)
			}
			s.requireLogs(t)
			if code != 1 {
				t.Errorf("exit %d, want 1; stdout = %q, stderr = %q", code, stdout, stderr)
			}
		})
	}
}

// The id echoed in the error is cut to 64 characters with every character outside [A-Za-z0-9-] shown as `?`.
func TestWaitDeployment_MalformedIDIsEchoedSanitised(t *testing.T) {
	cases := []struct{ name, id, want string }{
		{"metacharacters", "70f187fe;true", "70f187fe?true"},
		{"whitespace and newline", "a b\nc", "a?b?c"},
		{"command substitution", "$(id)", "??id?"},
		{"long", strings.Repeat("x", 200), strings.Repeat("x", 64)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newDepShim(t, depStatus(upIDA, "SUCCESS"))
			stdout, stderr, code := runWaitDeployment(t, s, waitDepExports(), c.id)
			if code != 1 {
				t.Fatalf("exit %d, want 1; stdout = %q, stderr = %q", code, stdout, stderr)
			}
			e := errorLines(stdout + stderr)
			if !strings.Contains(e, "'"+c.want+"'") {
				t.Errorf("error lines lack the sanitised id '%s': %q", c.want, e)
			}
			if strings.Contains(e, strings.Repeat("x", 65)) {
				t.Errorf("the id is not cut to 64 characters: %q", e)
			}
		})
	}
}

// The id checks run before the token check: a bad id names itself even with no token.
func TestWaitDeployment_IDChecksBeforeTheTokenCheck(t *testing.T) {
	for _, c := range []struct{ name, id, want string }{
		{"empty", "", "empty"},
		{"malformed", "not-a-uuid", "uuid"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newDepShim(t, depStatus(upIDA, "SUCCESS"))
			stdout, stderr, code := runWaitDeployment(t, s, forkExports(false, true, false), c.id)
			if code != 1 {
				t.Errorf("exit %d, want 1", code)
			}
			e := errorLines(stdout + stderr)
			if !strings.Contains(e, c.want) {
				t.Errorf("error lines lack %q: %q", c.want, e)
			}
			if strings.Contains(e, "RAILWAY_API_TOKEN") {
				t.Errorf("the token error masked the id error: %q", e)
			}
		})
	}
}

func TestWaitDeployment_MissingArguments(t *testing.T) {
	t.Run("no id", func(t *testing.T) {
		s := newDepShim(t, depStatus(upIDA, "SUCCESS"))
		stdout, stderr, code := s.run(t, waitDepExports(), "wait-deployment", waitDepLabel)
		if code != 1 {
			t.Errorf("exit %d, want 1; stdout = %q, stderr = %q", code, stdout, stderr)
		}
		if n := len(s.calls(t)); n != 0 {
			t.Errorf("calls = %d, want 0", n)
		}
		s.requireLogs(t)
		if e := errorLines(stdout + stderr); !strings.Contains(e, waitDepLabel) || !strings.Contains(e, "empty") {
			t.Errorf("error lines do not name the label and the empty id: %q", e)
		}
	})
	t.Run("no label", func(t *testing.T) {
		s := newDepShim(t, depStatus(upIDA, "SUCCESS"))
		stdout, stderr, code := s.run(t, waitDepExports(), "wait-deployment")
		if code != 2 {
			t.Errorf("exit %d, want 2 (usage); stdout = %q, stderr = %q", code, stdout, stderr)
		}
		if !strings.Contains(stdout+stderr, "usage: railway-env.sh wait-deployment <label> <deployment-id>") {
			t.Errorf("no wait-deployment usage line: %q", stdout+stderr)
		}
		if n := len(s.calls(t)); n != 0 {
			t.Errorf("calls = %d, want 0", n)
		}
		s.requireLogs(t)
	})
}

func TestWaitDeployment_NoProjectIDMakesNoCall(t *testing.T) {
	s := newDepShim(t, depStatus(upIDA, "SUCCESS"))
	stdout, stderr, code := runWaitDeployment(t, s, forkExports(true, false, false), upIDA)

	if n := len(s.calls(t)); n != 0 {
		t.Errorf("calls = %d, want 0 with no project id", n)
	}
	s.requireLogs(t)
	if code != 1 {
		t.Errorf("exit %d, want 1; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if e := errorLines(stdout + stderr); !strings.Contains(e, "RAILWAY_PROJECT_ID") {
		t.Errorf("error lines do not name RAILWAY_PROJECT_ID: %q", e)
	}
}

// No token reaches any output line, on the success line or on a failure path.
func TestWaitDeployment_NoTokenInAnyOutput(t *testing.T) {
	both := "export RAILWAY_PROJECT_TOKEN=" + waitDepToken + "\n" + forkExports(true, true, false)
	cases := []struct {
		name     string
		s        func(*testing.T) authShim
		wantCode int
	}{
		{"success", func(t *testing.T) authShim { return newDepShim(t, depStatus(upIDA, "SUCCESS")) }, 0},
		{"terminal status", func(t *testing.T) authShim { return newDepShim(t, depStatus(upIDA, "FAILED")) }, 1},
		{"graphql error", func(t *testing.T) authShim { return newDepShim(t, `{"errors":[{"message":"boom"}]}`) }, 1},
		{"third transient tick", func(t *testing.T) authShim {
			s := newDepShim(t, depStatus(upIDA, "SUCCESS"))
			setFaults(t, s, "dep", "timeout", "timeout", "timeout")
			return s
		}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := c.s(t)
			stdout, stderr, code := runWaitDeployment(t, s, both, upIDA)
			if code != c.wantCode {
				t.Fatalf("exit %d, want %d; stdout = %q, stderr = %q", code, c.wantCode, stdout, stderr)
			}
			for _, tok := range []string{forkToken, waitDepToken} {
				if strings.Contains(stdout+stderr, tok) {
					t.Errorf("output carries a token %q: stdout = %q, stderr = %q", tok, stdout, stderr)
				}
			}
			if c.wantCode == 0 {
				if want := "auth deployment " + upIDA + " is SUCCESS.\n"; stdout != want {
					t.Errorf("stdout = %q, want exactly %q", stdout, want)
				}
			}
		})
	}
}

// A transient final tick does not replace the last status read.
func TestWaitDeployment_WindowEndNamesLastRealStatusAfterFinalTransientTick(t *testing.T) {
	s := newDepShim(t, depStatus(upIDA, "DEPLOYING"))
	setFaults(t, s, "dep", append(strings.Fields(strings.Repeat("ok ", 59)), "timeout")...)
	stdout, stderr, code := runWaitDeployment(t, s, waitDepExports(), upIDA)

	if code != 1 {
		t.Errorf("exit %d, want 1; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if n := opCount(t, s, "dep"); n != 60 {
		t.Errorf("dep calls = %d, want 60", n)
	}
	e := errorLines(stdout + stderr)
	if !strings.Contains(e, "DEPLOYING") || strings.Contains(strings.ToUpper(e), "UNREADABLE") {
		t.Errorf("error lines must name DEPLOYING and not UNREADABLE: %q", e)
	}
}

func TestWaitDeployment_RateLimitedTickWaitsAndCounts(t *testing.T) {
	s := newDepShim(t, depStatus(upIDA, "SUCCESS"))
	setFaults(t, s, "dep", "429")
	plantRateLimitHeaders(t, s, "retry-after: 20")
	tmp := t.TempDir()
	stdout, stderr, code := runWaitDeployment(t, s, waitDepExports()+runnerTempExport(tmp), upIDA)

	if code != 0 {
		t.Errorf("exit %d, want 0: a poll tick's 429 waits and retries; stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if got := s.sleeps(t); !slices.Equal(got, []string{"20"}) {
		t.Errorf("sleeps = %v, want [20]", got)
	}
	if n := opCount(t, s, "dep"); n != 2 {
		t.Errorf("dep calls = %d, want 2 (the 429, then the retry of the same tick)", n)
	}
	if got := waitedTotal(t, tmp); got != "20" {
		t.Errorf("railway-api-429-waited = %q, want %q: a poll tick's wait counts toward the job total", got, "20")
	}
}
