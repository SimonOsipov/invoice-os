// railway_env_auth_secret_reread_test.go pins the exact secret re-read and the pre-write key check.
package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// A secret re-read that finds the inherited source value means the write did not land.
// Presence cannot tell the two apart: the fork always inherits AUTH_ADMIN_PASSWORD.
func TestSetForkAuth_SecretReReadMustEqualTheWrittenValue(t *testing.T) {
	k0 := freshJWK(t)
	for _, c := range []struct {
		name, svc, variable, source string
	}{
		{"GOTRUE_JWT_KEYS reads the source key", authForkAuthID, "GOTRUE_JWT_KEYS", k0},
		{"GOTRUE_JWT_SECRET reads the source secret", authForkAuthID, "GOTRUE_JWT_SECRET", authSourceJWTSecret},
		{"AUTH_ADMIN_PASSWORD reads the source password", authForkGatewayID, "AUTH_ADMIN_PASSWORD", authSourcePassword},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newForkAuthShim(t, k0)
			s.bendRead(t, c.svc, "."+c.variable+" = "+jqString(t, c.source))
			stdout, stderr, code := s.run(t, forkAuthExports(), "set-fork-auth", authForkEnvID)
			out := stdout + stderr
			if len(s.upserts(t)) == 0 {
				t.Fatal("control: nothing was written")
			}
			if code != 1 || !strings.Contains(errorLines(out), c.variable) {
				t.Errorf("exit %d and error lines %q, want exit 1 naming %s: the fork still holds the source value", code, errorLines(out), c.variable)
			}
			if strings.Contains(out, c.source) || strings.Contains(out, jwkPrivateScalar(t, k0)) {
				t.Error("the output carries the source value")
			}
		})
	}
}

// prenv is built before any write, so an operator's bad key can be refused before production holds it.
func TestSetProductionAuth_PostMergeRefusesAnInvalidKeyBeforeAnyWrite(t *testing.T) {
	a, b := freshJWK(t), freshJWK(t)
	var ka, kb []map[string]any
	if json.Unmarshal([]byte(a), &ka) != nil || json.Unmarshal([]byte(b), &kb) != nil {
		t.Fatal("jwk-es256 output is not a JSON array")
	}
	two, _ := json.Marshal(append(ka, kb...))
	delete(ka[0], "d")
	public, _ := json.Marshal(ka)

	for _, c := range []struct{ name, keys string }{
		{"an empty set", "[]"},
		{"two keys", string(two)},
		{"a public key only", string(public)},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, ok := jwkCheck(t, c.keys); ok {
				t.Fatal("control: jwk-check accepts this input")
			}
			s := newProdAuthShim(t, prodSealed())
			stdout, stderr, code := s.run(t, prodAuthExports(authProdPassword, c.keys, authProdJWTSecret, ""), "set-production-auth", "--post-merge", persistentEnvironmentID)
			out := stdout + stderr
			if code != 1 || !strings.Contains(errorLines(out), "AUTH_JWT_KEYS") {
				t.Errorf("exit %d and error lines %q, want exit 1 naming AUTH_JWT_KEYS", code, errorLines(out))
			}
			if m := s.mutations(t); len(m) != 0 {
				t.Errorf("production received %d mutation(s) before the bad key was refused", len(m))
			}
		})
	}
}

// Production before the seal: an older value on re-read means this write did not land.
func TestSetProductionAuth_SecretReReadMustEqualTheWrittenValue(t *testing.T) {
	older := freshJWK(t)
	for _, c := range []struct{ svc, variable, stale string }{
		{authProdAuthID, "GOTRUE_JWT_KEYS", older},
		{authProdAuthID, "GOTRUE_JWT_SECRET", authSourceJWTSecret},
		{authProdAuthID, "GOTRUE_SMTP_PASS", authSourceResendKey},
		{productionGatewayID, "AUTH_ADMIN_PASSWORD", authSourcePassword},
	} {
		t.Run(c.variable, func(t *testing.T) {
			s := newProdAuthShim(t, prodSealed())
			s.bendRead(t, c.svc, "."+c.variable+" = "+jqString(t, c.stale))
			stdout, stderr, code := s.run(t, prodAuthExports(authProdPassword, freshJWK(t), authProdJWTSecret, authProdResendKey), "set-production-auth", "--post-merge", persistentEnvironmentID)
			out := stdout + stderr
			if code != 1 || !strings.Contains(errorLines(out), c.variable+" reads a different value") {
				t.Errorf("exit %d and error lines %q, want exit 1: %s reads a different value", code, errorLines(out), c.variable)
			}
			if strings.Contains(out, c.stale) {
				t.Error("the output carries the value it read")
			}
		})
	}
}
