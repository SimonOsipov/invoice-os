// railway_env_reconciliation_validation_test.go drives the reconciliation_validation pass of fork-vars-before-urls against a scripted Railway.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	// VALIDATION_INTERNAL_URL in scripts/ci/railway-env.sh.
	validationInternalURL = "http://validation.railway.internal:8080"
	// A Railway reference Railway expands itself; the pass must write it literally.
	validationTokenRef = "${{validation.S2S_TOKEN}}"
)

var reconciliationSvcID = gtSvcID("reconciliation")

func newValidationShim(t *testing.T, resp map[string]string, reconciliation map[string]string) authShim {
	t.Helper()
	s := newPassShim(t, nil, resp)
	if reconciliation != nil {
		raw := `{`
		sep := ""
		for k, v := range reconciliation {
			raw += sep + `"` + k + `":"` + v + `"`
			sep = ","
		}
		writeFile(t, filepath.Join(s.dir, "store-"+reconciliationSvcID+".json"), raw+`}`)
	}
	return s
}

func reconciliationWrites(t *testing.T, s authShim) []collectionWrite {
	t.Helper()
	return writesTo(passWrites(t, s), reconciliationSvcID)
}

func TestReconciliationValidationPass_WritesBothVariables(t *testing.T) {
	s := newValidationShim(t, nil, nil)
	out, code := runPass(t, s)
	if code != 0 {
		t.Fatalf("exit %d, want 0; output = %q", code, out)
	}
	ws := reconciliationWrites(t, s)
	if len(ws) != 1 {
		t.Fatalf("reconciliation writes = %v, want exactly one", writeNames(ws))
	}
	w := ws[0]
	if w.SkipDeploys != true || w.Env != authForkEnvID {
		t.Errorf("write: skipDeploys=%v environmentId=%v, want true and %s", w.SkipDeploys, w.Env, authForkEnvID)
	}
	if len(w.Vars) != 2 || w.Vars["VALIDATION_URL"] != validationInternalURL || w.Vars["S2S_TOKEN"] == "" {
		t.Errorf("write carries %v, want exactly VALIDATION_URL=%s and S2S_TOKEN", writeNames(ws), validationInternalURL)
	}
	if len(callsOf(s, t, "varsWrite")) != 1 {
		t.Errorf("varsWrite calls = %d, want one batched write", len(callsOf(s, t, "varsWrite")))
	}
	if at := s.lastCallIndex(t, reconciliationSvcID, "VALIDATION_URL"); at < 0 || !s.readAfter(t, reconciliationSvcID, at) {
		t.Errorf("reconciliation was not re-read after the write (write at call %d)", at)
	}
	found := false
	for _, l := range confirmedLines(out) {
		if wordIn(l, "reconciliation.VALIDATION_URL") && wordIn(l, "reconciliation.S2S_TOKEN") && strings.Contains(l, authForkEnvID) {
			found = true
		}
	}
	if !found {
		t.Errorf("no confirmation line names both variables and the fork; output = %q", out)
	}
}

func TestReconciliationValidationPass_TokenIsAReferenceNotAValue(t *testing.T) {
	s := newValidationShim(t, nil, nil)
	if out, code := runPass(t, s); code != 0 {
		t.Fatalf("exit %d, want 0; output = %q", code, out)
	}
	v, ok := passWritten(passWrites(t, s), reconciliationSvcID, "S2S_TOKEN")
	if !ok || v != validationTokenRef {
		t.Errorf("S2S_TOKEN written as %q (written=%v), want the literal %q", v, ok, validationTokenRef)
	}
}

func TestReconciliationValidationPass_RefusesThePersistentEnvironment(t *testing.T) {
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
		"export RAILWAY_DEV_ENVIRONMENT_ID="+persistentEnvironmentID+"\nsource '"+lib+"'\nreconciliation_validation_check \"$@\"\necho survived\n",
		persistentEnvironmentID)
	out := stdout + stderr
	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, out)
	}
	if e := errorLines(out); !strings.Contains(e, "Refusing") || !strings.Contains(e, "VALIDATION_URL") || !strings.Contains(e, persistentEnvironmentID) {
		t.Errorf("no ::error:: line refuses VALIDATION_URL in %s; output = %q", persistentEnvironmentID, out)
	}
	if strings.Contains(out, "survived") {
		t.Errorf("the check returned; output = %q", out)
	}

	// Through the command the persistent id writes nothing.
	s := newValidationShim(t, nil, nil)
	stdout, stderr, code = s.run(t, forkAuthExports(), passSub, persistentEnvironmentID)
	if code != 1 {
		t.Errorf("command exit %d, want 1; output = %q", code, stdout+stderr)
	}
	if ups := s.upserts(t); len(ups) != 0 {
		t.Errorf("the persistent id received upserts %v", names(ups))
	}
}

func TestReconciliationValidationPass_NoWriteWhenAlreadySet(t *testing.T) {
	s := newValidationShim(t, nil, map[string]string{"VALIDATION_URL": validationInternalURL, "S2S_TOKEN": validationTokenRef})
	out, code := runPass(t, s)
	if code != 0 {
		t.Fatalf("exit %d, want 0; output = %q", code, out)
	}
	if ws := reconciliationWrites(t, s); len(ws) != 0 {
		t.Errorf("reconciliation was written %v though both values matched", writeNames(ws))
	}
	if !strings.Contains(out, "reconciliation: 2 of 2 already hold the intended value") {
		t.Errorf("no line says both held; output = %q", out)
	}
}

func TestReconciliationValidationPass_FailedWriteIsAnError(t *testing.T) {
	s := newValidationShim(t, nil, nil)
	s.failAlias(t, reconciliationSvcID, "abort")
	out, code := runPass(t, s)
	if code != 1 {
		t.Fatalf("exit %d, want 1; output = %q", code, out)
	}
	e := errorLines(out)
	if !strings.Contains(e, "reconciliation") || !strings.Contains(e, "VALIDATION_URL") {
		t.Errorf("no ::error:: line names reconciliation and VALIDATION_URL; output = %q", out)
	}
	if at := s.lastCallIndex(t, reconciliationSvcID, "VALIDATION_URL"); at < 0 || s.readAfter(t, reconciliationSvcID, at) {
		t.Errorf("a failed write was followed by a re-read (write at call %d)", at)
	}
	for _, l := range confirmedLines(out) {
		if strings.Contains(l, "VALIDATION_URL") {
			t.Errorf("a failed run confirmed VALIDATION_URL: %q", l)
		}
	}
}

func TestReconciliationValidationPass_MissingServiceIsAnError(t *testing.T) {
	s := newValidationShim(t, map[string]string{"settle": gtSettle(t, "reconciliation")}, nil)
	out, code := runPass(t, s)
	if code != 1 {
		t.Fatalf("exit %d, want 1; output = %q", code, out)
	}
	if !strings.Contains(errorLines(out), "VALIDATION_URL was NOT set") {
		t.Errorf("no ::error:: line says VALIDATION_URL was NOT set; output = %q", out)
	}
	if ups := s.upserts(t); len(ups) != 0 {
		t.Errorf("a missing service was followed by upserts %v", names(ups))
	}
}
