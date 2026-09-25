package main

import (
	"strings"
	"testing"
)

// Driven sibling of TestSetForkAuthAutoconfirms: the value on the wire, the service, and the read-back.
func TestSetForkAuth_AutoconfirmsOnAuthOnly(t *testing.T) {
	s, _ := runForkAuthOK(t, freshJWK(t))
	ups := s.upserts(t)
	if len(upsertsOf(ups, authForkAuthID, "GOTRUE_DISABLE_SIGNUP")) != 1 {
		t.Fatal("control: auth.GOTRUE_DISABLE_SIGNUP was not written once")
	}
	if got := oneUpsert(t, ups, authForkAuthID, "GOTRUE_MAILER_AUTOCONFIRM"); got != "true" {
		t.Errorf("auth.GOTRUE_MAILER_AUTOCONFIRM = %q, want \"true\"", got)
	}
	if at := s.lastCallIndex(t, authForkAuthID, "GOTRUE_MAILER_AUTOCONFIRM"); at < 0 || !s.readAfter(t, authForkAuthID, at) {
		t.Error("auth's variables were not re-read after the GOTRUE_MAILER_AUTOCONFIRM write")
	}
	if got := upsertsOf(ups, authForkGatewayID, "GOTRUE_MAILER_AUTOCONFIRM"); len(got) != 0 {
		t.Errorf("gateway received GOTRUE_MAILER_AUTOCONFIRM %v; it belongs to auth only", got)
	}
}

// Railway may hold a different value after the write; the run must fail and name the variable.
func TestSetForkAuth_AutoconfirmReReadMismatchFails(t *testing.T) {
	for _, c := range []struct{ name, filter string }{
		{"reads false", `.GOTRUE_MAILER_AUTOCONFIRM = "false"`},
		{"reads TRUE", `.GOTRUE_MAILER_AUTOCONFIRM = "TRUE"`},
		{"absent", `del(.GOTRUE_MAILER_AUTOCONFIRM)`},
		{"empty", `.GOTRUE_MAILER_AUTOCONFIRM = ""`},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := newForkAuthShim(t, freshJWK(t))
			s.bendRead(t, authForkAuthID, c.filter)
			stdout, stderr, code := s.run(t, forkAuthExports(), "set-fork-auth", authForkEnvID)
			out := stdout + stderr
			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, out)
			}
			if !strings.Contains(errorLines(out), "GOTRUE_MAILER_AUTOCONFIRM") {
				t.Errorf("no ::error:: line names GOTRUE_MAILER_AUTOCONFIRM; error lines = %q", errorLines(out))
			}
			if len(upsertsOf(s.upserts(t), authForkAuthID, "GOTRUE_MAILER_AUTOCONFIRM")) == 0 {
				t.Error("GOTRUE_MAILER_AUTOCONFIRM was never written, so the failure is not a re-read failure")
			}
		})
	}
	// Control: an unbent read passes, so the failures above come from the bend.
	runForkAuthOK(t, freshJWK(t))
}
