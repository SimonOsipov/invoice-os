// railway_env_auth_adversarial_test.go covers the auth writers' edges the AC tests leave open:
// argv transport, failed upserts, the unrendered re-read, sealed-read shapes and refusals.
package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// jqArgvLog puts a logging jq ahead of the real one and returns the log path.
func jqArgvLog(t *testing.T, s authShim) string {
	t.Helper()
	real, err := exec.LookPath("jq")
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(s.dir, "jq-argv.log")
	writeFile(t, filepath.Join(s.dir, "jq"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+log+"'\nexec '"+real+"' \"$@\"\n")
	if err := os.Chmod(filepath.Join(s.dir, "jq"), 0o755); err != nil {
		t.Fatal(err)
	}
	return log
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(raw)
}

func jqString(t *testing.T, v string) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestAuthSecretsNeverReachJQArgv(t *testing.T) {
	scan := func(t *testing.T, log string, needles map[string]string) {
		t.Helper()
		argv := readLog(t, log)
		if !strings.Contains(argv, "variableUpsert") {
			t.Fatalf("control: the jq log holds no upsert body, so a clean scan proves nothing")
		}
		for label, n := range needles {
			if n == "" {
				t.Errorf("needle %s is empty", label)
				continue
			}
			if strings.Contains(argv, n) {
				t.Errorf("jq's argv carries %s; a secret must reach jq on stdin (visible in ps)", label)
			}
		}
	}

	t.Run("set-fork-auth", func(t *testing.T) {
		s := newForkAuthShim(t, freshJWK(t))
		log := jqArgvLog(t, s)
		stdout, stderr, code := s.run(t, forkAuthExports(), "set-fork-auth", authForkEnvID)
		if code != 0 {
			t.Fatalf("exit %d, want 0; output = %q", code, stdout+stderr)
		}
		ups := s.upserts(t)
		key := oneUpsert(t, ups, authForkAuthID, "GOTRUE_JWT_KEYS")
		scan(t, log, map[string]string{
			"the generated key's private scalar": jwkPrivateScalar(t, key),
			"the generated JWT secret":           oneUpsert(t, ups, authForkAuthID, "GOTRUE_JWT_SECRET"),
			"the generated admin password":       oneUpsert(t, ups, authForkGatewayID, "AUTH_ADMIN_PASSWORD"),
		})
	})

	t.Run("set-production-auth --post-merge", func(t *testing.T) {
		jwk := freshJWK(t)
		s := newProdAuthShim(t, prodSealed())
		log := jqArgvLog(t, s)
		stdout, stderr, code := s.run(t, prodAuthExports(authProdPassword, jwk, authProdJWTSecret, authProdResendKey), "set-production-auth", "--post-merge", persistentEnvironmentID)
		if code != 0 {
			t.Fatalf("exit %d, want 0; output = %q", code, stdout+stderr)
		}
		scan(t, log, map[string]string{
			"the key's private scalar": jwkPrivateScalar(t, jwk),
			"the JWT secret":           authProdJWTSecret,
			"the admin password":       authProdPassword,
			"the Resend key":           authProdResendKey,
		})
	})
}

func TestSetForkAuth_SecretUpsertFailureExitsAndNamesIt(t *testing.T) {
	k0 := freshJWK(t)
	for _, c := range []struct {
		name, variable, file, body string
	}{
		// The fork still holds the inherited key, so only the upsert's own exit can fail the run.
		{"transport, over an inherited key", "GOTRUE_JWT_KEYS", "upsert-GOTRUE_JWT_KEYS.fail", "curl: (22) The requested URL returned error: 500"},
		{"graphql error", "AUTH_ADMIN_PASSWORD", "upsert-AUTH_ADMIN_PASSWORD.json", `{"errors":[{"message":"planted refusal"}]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newForkAuthShim(t, k0)
			writeFile(t, filepath.Join(s.dir, c.file), c.body)
			stdout, stderr, code := s.run(t, forkAuthExports(), "set-fork-auth", authForkEnvID)
			out := stdout + stderr
			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, out)
			}
			if !strings.Contains(errorLines(out), c.variable) {
				t.Errorf("no ::error:: line names %s; error lines = %q", c.variable, errorLines(out))
			}
			if strings.Contains(out, c.variable+" = <redacted>") {
				t.Errorf("the failed upsert still printed its redacted success line")
			}
			for label, n := range map[string]string{
				"the source key's private scalar": jwkPrivateScalar(t, k0),
				"the source admin password":       authSourcePassword,
				"the source JWT secret":           authSourceJWTSecret,
			} {
				if strings.Contains(out, n) {
					t.Errorf("the output carries %s", label)
				}
			}
			for _, u := range s.upserts(t) {
				if u.Name == "GOTRUE_JWT_SECRET" && strings.Contains(out, u.Value) {
					t.Error("the output carries the generated JWT secret")
				}
			}
		})
	}
}

func TestSetForkAuth_ReReadIsUnrenderedAndNeverPrintsARenderedDSN(t *testing.T) {
	t.Run("every re-read asks for unrendered values", func(t *testing.T) {
		s, _ := runForkAuthOK(t, freshJWK(t))
		reads := 0
		for _, c := range s.calls(t) {
			if !strings.Contains(c.Query, "variables(projectId") {
				continue
			}
			reads++
			if !regexp.MustCompile(`unrendered:\s*true`).MatchString(c.Query) {
				t.Errorf("a variables read omits unrendered: true, so DATABASE_URL would read back rendered: %q", c.Query)
			}
		}
		if reads != 2 {
			t.Errorf("%d variables reads, want 2 (auth and gateway)", reads)
		}
	})

	t.Run("a rendered DATABASE_URL fails without printing it", func(t *testing.T) {
		const renderedPW = "rendered0000password0000planted"
		s := newForkAuthShim(t, freshJWK(t))
		s.bendRead(t, authForkAuthID, `.DATABASE_URL = "postgresql://supabase_auth_admin:`+renderedPW+`@postgres.railway.internal:5432/railway"`)
		stdout, stderr, code := s.run(t, forkAuthExports(), "set-fork-auth", authForkEnvID)
		out := stdout + stderr
		if code != 1 || !strings.Contains(errorLines(out), "DATABASE_URL") {
			t.Errorf("exit %d and error lines %q, want exit 1 naming DATABASE_URL", code, errorLines(out))
		}
		if strings.Contains(out, renderedPW) {
			t.Error("the output carries the rendered password")
		}
	})
}

func TestSetForkAuth_ReReadEdges(t *testing.T) {
	for _, c := range []struct {
		name, svc, variable, filter, needle string
	}{
		// $(...) strips a trailing newline; the compare must not.
		{"AUTH_URL with a trailing newline", authForkGatewayID, "AUTH_URL", `.AUTH_URL = "` + authInternalURL + `\n"`, ""},
		{"GOTRUE_SMTP_HOST absent, not blank", authForkAuthID, "GOTRUE_SMTP_HOST", `del(.GOTRUE_SMTP_HOST)`, ""},
		{"GOTRUE_SMTP_PASS reads the source key", authForkAuthID, "GOTRUE_SMTP_PASS", `.GOTRUE_SMTP_PASS = "` + authSourceResendKey + `"`, authSourceResendKey},
		{"GOTRUE_JWT_SECRET with a trailing newline", authForkAuthID, "GOTRUE_JWT_SECRET", `.GOTRUE_JWT_SECRET += "\n"`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newForkAuthShim(t, freshJWK(t))
			s.bendRead(t, c.svc, c.filter)
			stdout, stderr, code := s.run(t, forkAuthExports(), "set-fork-auth", authForkEnvID)
			out := stdout + stderr
			if code != 1 || !strings.Contains(errorLines(out), c.variable) {
				t.Errorf("exit %d and error lines %q, want exit 1 naming %s", code, errorLines(out), c.variable)
			}
			if c.needle != "" && strings.Contains(out, c.needle) {
				t.Error("the failed re-read printed the value it read")
			}
		})
	}
}

// AC-3 names absent, empty and mismatch as distinct failures; the error says which.
func TestAuthReReadSaysAbsentOrEmpty(t *testing.T) {
	for _, c := range []struct{ name, svc, filter, says string }{
		{"a non-secret absent", authForkGatewayID, `del(.AUTH_URL)`, "gateway.AUTH_URL is absent"},
		{"a secret absent", authForkAuthID, `del(.GOTRUE_JWT_SECRET)`, "auth.GOTRUE_JWT_SECRET is absent"},
		{"a secret empty", authForkGatewayID, `.AUTH_ADMIN_PASSWORD = ""`, "gateway.AUTH_ADMIN_PASSWORD is empty"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := newForkAuthShim(t, freshJWK(t))
			s.bendRead(t, c.svc, c.filter)
			stdout, stderr, code := s.run(t, forkAuthExports(), "set-fork-auth", authForkEnvID)
			if code != 1 || !strings.Contains(errorLines(stdout+stderr), c.says) {
				t.Errorf("exit %d and error lines %q, want exit 1 saying %q", code, errorLines(stdout+stderr), c.says)
			}
		})
	}
}

// The fork has no pre-write key check, so the read-back jwk-check is the only guard on the generator.
func TestSetForkAuth_AnInvalidGeneratedKeyFails(t *testing.T) {
	s := newForkAuthShim(t, freshJWK(t))
	fake := "#!/bin/sh\nif [ \"$1\" = jwk-es256 ]; then echo '[]'; exit 0; fi\nexec '" + binPath + "' \"$@\"\n"
	goShim := "#!/bin/sh\nout=''\nwhile [ $# -gt 0 ]; do [ \"$1\" = -o ] && out=\"$2\"; shift; done\n" +
		"cat > \"$out\" <<'FAKE'\n" + fake + "FAKE\nchmod +x \"$out\"\n"
	writeFile(t, filepath.Join(s.dir, "go"), goShim)
	if err := os.Chmod(filepath.Join(s.dir, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := s.run(t, forkAuthExports(), "set-fork-auth", authForkEnvID)
	out := stdout + stderr
	if got := oneUpsert(t, s.upserts(t), authForkAuthID, "GOTRUE_JWT_KEYS"); got != "[]" {
		t.Fatalf("control: the fake generator did not run; key written = %q", got)
	}
	if code != 1 || !strings.Contains(errorLines(out), "GOTRUE_JWT_KEYS is not exactly one ES256 signing key") {
		t.Errorf("exit %d and error lines %q, want exit 1 refusing GOTRUE_JWT_KEYS", code, errorLines(out))
	}
}

func TestSetForkAuth_RefusesANamelessEnvironment(t *testing.T) {
	resp := forkAuthRailway()
	resp["envList"] = strings.Replace(authEnvList(true), `"name":"`+authForkName+`",`, "", 1)
	if resp["envList"] == authEnvList(true) {
		t.Fatal("control: the fork's name was not removed")
	}
	s := newAuthShim(t, resp, forkAuthStores(freshJWK(t)))
	stdout, stderr, code := s.run(t, forkAuthExports(), "set-fork-auth", authForkEnvID)
	out := stdout + stderr
	if code != 1 || !strings.Contains(errorLines(out), "no name") {
		t.Errorf("exit %d and error lines %q, want exit 1 saying the environment has no name", code, errorLines(out))
	}
	if m := s.mutations(t); len(m) != 0 {
		t.Errorf("a nameless environment received mutations %v; its issuer would be urn:ascomply:auth:", m)
	}
}

func TestSetForkAuthSite_MoreRefusals(t *testing.T) {
	for _, c := range []struct {
		name, env, url, says string
	}{
		{"a non-ephemeral id", authStaleEnvID, "https://landing-pr-8.up.railway.app", "is NOT ephemeral"},
		{"a bare https scheme", authForkEnvID, "https://", "https://"},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp := forkAuthRailway()
			resp["envList"] = authEnvList(false)
			s := newAuthShim(t, resp, forkAuthStores(freshJWK(t)))
			stdout, stderr, code := s.run(t, forkAuthExports(), "set-fork-auth-site", c.env, c.url)
			out := stdout + stderr
			if code != 1 || !strings.Contains(errorLines(out), c.says) {
				t.Errorf("exit %d and error lines %q, want exit 1 carrying %q", code, errorLines(out), c.says)
			}
			if m := s.mutations(t); len(m) != 0 {
				t.Errorf("the refusal wrote %v", m)
			}
		})
	}
}

func TestSetForkAuth_AdminPasswordIsFreshPerRun(t *testing.T) {
	s1, _ := runForkAuthOK(t, freshJWK(t))
	s2, _ := runForkAuthOK(t, freshJWK(t))
	p1 := oneUpsert(t, s1.upserts(t), authForkGatewayID, "AUTH_ADMIN_PASSWORD")
	p2 := oneUpsert(t, s2.upserts(t), authForkGatewayID, "AUTH_ADMIN_PASSWORD")
	if p1 == "" {
		t.Fatal("no admin password was written")
	}
	if p1 == p2 {
		t.Error("two runs wrote the same AUTH_ADMIN_PASSWORD; it is not generated per run")
	}
}

func TestSetProductionAuth_SealedReadEdges(t *testing.T) {
	exports := prodAuthExports(authProdPassword, freshJWK(t), authProdJWTSecret, authProdResendKey)

	t.Run("a null environment refuses before any write", func(t *testing.T) {
		s := newProdAuthShim(t, `{"data":{"environment":null}}`)
		stdout, stderr, code := s.run(t, exports, "set-production-auth", "--post-merge", persistentEnvironmentID)
		out := stdout + stderr
		if code != 1 || !strings.Contains(errorLines(out), "null environment") {
			t.Errorf("exit %d and error lines %q, want exit 1 naming the null environment", code, errorLines(out))
		}
		if m := s.mutations(t); len(m) != 0 {
			t.Errorf("an unread sealed flag was followed by mutations %v", m)
		}
	})

	t.Run("the same name sealed on another service does not refuse", func(t *testing.T) {
		sealed := `{"data":{"environment":{"id":"` + persistentEnvironmentID + `","name":"production","variables":{"edges":[` +
			`{"node":{"name":"GOTRUE_JWT_KEYS","isSealed":true,"serviceId":"` + productionGatewayID + `"}}]}}}}`
		s := newProdAuthShim(t, sealed)
		stdout, stderr, code := s.run(t, exports, "set-production-auth", "--post-merge", persistentEnvironmentID)
		if code != 0 {
			t.Errorf("exit %d, want 0: only auth's own sealed targets refuse; output = %q", code, stdout+stderr)
		}
	})
}

// Pre-merge needs no secret, so it must not reach the sealed read or the post-merge writes.
func TestSetProductionAuth_PreMergeCallsOnlySettleWriteAndReRead(t *testing.T) {
	s := newProdAuthShim(t, prodSealed())
	stdout, stderr, code := s.run(t, forkExports(true, true, true), "set-production-auth", "--pre-merge", persistentEnvironmentID)
	if code != 0 {
		t.Fatalf("exit %d, want 0; output = %q", code, stdout+stderr)
	}
	if got, want := operations(s.calls(t)), []string{"settle", "varUpsert", "authVars"}; !slices.Equal(got, want) {
		t.Errorf("Railway calls = %v, want %v", got, want)
	}
}

func TestSetForkAuth_PrenvBuildFailureWritesNothing(t *testing.T) {
	s := newForkAuthShim(t, freshJWK(t))
	writeFile(t, filepath.Join(s.dir, "go"), "#!/bin/sh\necho 'go: planted build failure' >&2\nexit 1\n")
	if err := os.Chmod(filepath.Join(s.dir, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := s.run(t, forkAuthExports(), "set-fork-auth", authForkEnvID)
	out := stdout + stderr
	if code != 1 || !strings.Contains(errorLines(out), "Could not build tools/prenv") {
		t.Errorf("exit %d and error lines %q, want exit 1 naming the prenv build", code, errorLines(out))
	}
	if m := s.mutations(t); len(m) != 0 {
		t.Errorf("a failed prenv build was followed by mutations %v", m)
	}
}
