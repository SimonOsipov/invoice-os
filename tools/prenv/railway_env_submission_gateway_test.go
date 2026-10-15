// railway_env_submission_gateway_test.go drives the submission_gateway pass of fork-vars-before-urls against a scripted Railway.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GATEWAY_INTERNAL_URL in scripts/ci/railway-env.sh.
const gatewayInternalURL = "http://gateway.railway.internal:8080"

var submissionSvcID = gtSvcID("submission")

func newSubmissionShim(t *testing.T, submission map[string]string) authShim {
	t.Helper()
	s := newPassShim(t, nil, nil)
	if submission != nil {
		raw := `{`
		sep := ""
		for k, v := range submission {
			raw += sep + `"` + k + `":"` + v + `"`
			sep = ","
		}
		writeFile(t, filepath.Join(s.dir, "store-"+submissionSvcID+".json"), raw+`}`)
	}
	return s
}

func submissionURLWrites(t *testing.T, s authShim) []collectionWrite {
	t.Helper()
	var out []collectionWrite
	for _, w := range writesTo(passWrites(t, s), submissionSvcID) {
		if _, ok := w.Vars["GATEWAY_URL"]; ok {
			out = append(out, w)
		}
	}
	return out
}

func requireSubmissionConfirmed(t *testing.T, out string) {
	t.Helper()
	for _, l := range confirmedLines(out) {
		if wordIn(l, "submission.GATEWAY_URL") && strings.Contains(l, authForkEnvID) {
			return
		}
	}
	t.Errorf("no confirmation line names submission.GATEWAY_URL and the fork; output = %q", out)
}

func TestSubmissionGatewayPass_WritesTheInternalURL(t *testing.T) {
	s := newSubmissionShim(t, nil)
	out, code := runPass(t, s)
	if code != 0 {
		t.Fatalf("exit %d, want 0; output = %q", code, out)
	}
	ws := submissionURLWrites(t, s)
	if len(ws) != 1 {
		t.Fatalf("submission GATEWAY_URL writes = %v, want exactly one", writeNames(ws))
	}
	w := ws[0]
	if w.SkipDeploys != true || w.Env != authForkEnvID {
		t.Errorf("write: skipDeploys=%v environmentId=%v, want true and %s", w.SkipDeploys, w.Env, authForkEnvID)
	}
	if v := w.Vars["GATEWAY_URL"]; v != gatewayInternalURL {
		t.Errorf("GATEWAY_URL written as %q, want %q", v, gatewayInternalURL)
	}
	if at := s.lastCallIndex(t, submissionSvcID, "GATEWAY_URL"); at < 0 || !s.readAfter(t, submissionSvcID, at) {
		t.Errorf("submission was not re-read after the write (write at call %d)", at)
	}
	requireSubmissionConfirmed(t, out)
}

func TestSubmissionGatewayPass_SkipsAValueThatHolds(t *testing.T) {
	s := newSubmissionShim(t, map[string]string{"GATEWAY_URL": gatewayInternalURL})
	out, code := runPass(t, s)
	if code != 0 {
		t.Fatalf("exit %d, want 0; output = %q", code, out)
	}
	if ws := submissionURLWrites(t, s); len(ws) != 0 {
		t.Errorf("GATEWAY_URL was written %v though it held", writeNames(ws))
	}
	requireSubmissionConfirmed(t, out)
}

func TestSubmissionGatewayPass_RefusesThePersistentEnvironment(t *testing.T) {
	// fork_auth's own refusal runs first through the command, so call this pass's check alone.
	raw, err := os.ReadFile(railwayEnvScript(t))
	if err != nil {
		t.Fatal(err)
	}
	head, _, ok := strings.Cut(string(raw), "\ncase \"${1:-}\" in\n")
	if !ok {
		t.Fatal("railway-env.sh has no dispatcher case to cut before")
	}
	lib := filepath.Join(t.TempDir(), "railway-env-lib.sh")
	writeFile(t, lib, head)
	stdout, stderr, code := runBashScript(t,
		"export RAILWAY_DEV_ENVIRONMENT_ID="+persistentEnvironmentID+"\nsource '"+lib+"'\nsubmission_gateway_check \"$@\"\necho survived\n",
		persistentEnvironmentID)
	out := stdout + stderr
	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, out)
	}
	if e := errorLines(out); !strings.Contains(e, "Refusing") || !strings.Contains(e, "GATEWAY_URL") || !strings.Contains(e, persistentEnvironmentID) {
		t.Errorf("no ::error:: line refuses GATEWAY_URL in %s; output = %q", persistentEnvironmentID, out)
	}
	if strings.Contains(out, "survived") {
		t.Errorf("the check returned; output = %q", out)
	}

	s := newSubmissionShim(t, nil)
	stdout, stderr, code = s.run(t, forkAuthExports(), passSub, persistentEnvironmentID)
	if code != 1 {
		t.Errorf("command exit %d, want 1; output = %q", code, stdout+stderr)
	}
	if ups := s.upserts(t); len(ups) != 0 {
		t.Errorf("the persistent id received upserts %v", names(ups))
	}
	if m := s.mutations(t); len(m) != 0 {
		t.Errorf("the persistent id received mutations %v", m)
	}
}

func TestSubmissionGatewayPass_FailsLoudly(t *testing.T) {
	for _, c := range []struct {
		name  string
		names string // the shared unreadable-service error names the service, not the variable
		why   string // the step's own error, not the verdict that would follow it anyway
		setup func(t *testing.T, s authShim)
	}{
		{"write refused", "GATEWAY_URL", "batched variable write", func(t *testing.T, s authShim) { s.failAlias(t, submissionSvcID, "abort") }},
		{"transport failure on the re-read", "submission", "written but not confirmed", func(t *testing.T, s authShim) { s.plantReadBad(t, "after", submissionSvcID, "error") }},
		{"re-read absent", "GATEWAY_URL", "", func(t *testing.T, s authShim) { s.bendReRead(t, submissionSvcID, `del(.GATEWAY_URL)`) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newSubmissionShim(t, nil)
			c.setup(t, s)
			out, code := runPass(t, s)
			if len(passWrites(t, s)) == 0 {
				t.Fatal("control: nothing was written")
			}
			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, out)
			}
			if e := errorLines(out); !strings.Contains(e, "submission") || !strings.Contains(e, c.names) {
				t.Errorf("no ::error:: line names submission and %s; output = %q", c.names, out)
			}
			if !strings.Contains(errorLines(out), c.why) {
				t.Errorf("no ::error:: line carries %q; output = %q", c.why, out)
			}
			for _, l := range confirmedLines(out) {
				if strings.Contains(l, "submission.GATEWAY_URL") {
					t.Errorf("a failed run confirmed GATEWAY_URL: %q", l)
				}
			}
		})
	}
}
