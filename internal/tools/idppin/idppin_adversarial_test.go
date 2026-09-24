package main

import (
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	pinnedDigest = "sha256:1736a63078f5922b198c4cbe50f80ab9a2d3b54fe8b7b6cfb2e9dc5dbbc12c6b"
	pinnedRef    = "ghcr.io/supabase/auth:v2.197.0@" + pinnedDigest
)

func TestParsePinAdversarial(t *testing.T) {
	if got, err := ParsePin("FROM " + pinnedRef + "\n"); err != nil || got != pinnedTag {
		t.Fatalf("control: ParsePin = %q, %v; want %q, nil", got, err, pinnedTag)
	}
	hex := strings.TrimPrefix(pinnedDigest, "sha256:")

	accepted := []struct{ name, body string }{
		{"--platform flag before the reference", "FROM --platform=linux/amd64 " + pinnedRef + "\n"},
		{"trailing AS stage", "FROM " + pinnedRef + " AS base\n"},
		{"--platform and AS together", "FROM --platform=$BUILDPLATFORM " + pinnedRef + " as base\n"},
		{"lowercase from", "from " + pinnedRef + "\n"},
		{"CRLF line endings", "FROM " + pinnedRef + "\r\nENV A=b\r\n"},
		{"a commented FROM does not count", "# FROM docker.io/supabase/auth:v1@" + pinnedDigest + "\nFROM " + pinnedRef + "\n"},
		{"registry with a port", "FROM ghcr.io:443/supabase/auth:v2.197.0@" + pinnedDigest + "\n"},
	}
	for _, c := range accepted {
		t.Run("accepts/"+c.name, func(t *testing.T) {
			if got, err := ParsePin(c.body); err != nil || got != pinnedTag {
				t.Errorf("ParsePin = %q, %v; want %q, nil\n%s", got, err, pinnedTag, c.body)
			}
		})
	}

	refused := []struct{ name, body string }{
		{"sha512 digest", "FROM ghcr.io/supabase/auth:v2.197.0@sha512:" + hex + hex + "\n"},
		{"uppercase digest hex", "FROM ghcr.io/supabase/auth:v2.197.0@sha256:" + strings.ToUpper(hex) + "\n"},
		{"63-char digest", "FROM ghcr.io/supabase/auth:v2.197.0@sha256:" + hex[1:] + "\n"},
		{"digest but no tag", "FROM ghcr.io/supabase/auth@" + pinnedDigest + "\n"},
		{"index.docker.io", "FROM index.docker.io/supabase/auth:v2.197.0@" + pinnedDigest + "\n"},
		{"registry-1.docker.io", "FROM registry-1.docker.io/supabase/auth:v2.197.0@" + pinnedDigest + "\n"},
		{"library image, no namespace", "FROM auth:v2.197.0@" + pinnedDigest + "\n"},
		{"--platform with a Hub name", "FROM --platform=linux/amd64 supabase/auth:v2.197.0@" + pinnedDigest + "\n"},
		{"AS stage after an unpinned ref", "FROM ghcr.io/supabase/auth:v2.197.0 AS base\n"},
		{"FROM with no reference", "FROM\n"},
		{"multi-stage second FROM", "FROM " + pinnedRef + " AS a\nFROM a\n"},
	}
	for _, c := range refused {
		t.Run("refuses/"+c.name, func(t *testing.T) {
			if got, err := ParsePin(c.body); err == nil {
				t.Errorf("ParsePin accepted it and returned %q; want an error\n%s", got, c.body)
			}
		})
	}
}

// A refusal must print nothing on stdout: CI captures it into IDP_PINNED_TAG.
func TestIdppinRefusalsExit2WithEmptyStdout(t *testing.T) {
	bad := writeDockerfile(t, "FROM supabase/auth:v2.197.0@"+pinnedDigest+"\n")
	good := writeDockerfile(t, plannedBody)
	cases := []struct {
		name string
		args []string
	}{
		{"tag on a Hub FROM", []string{"tag", bad}},
		{"tag on a missing file", []string{"tag", bad + ".missing"}},
		{"latest-check on a Hub FROM, same tag", []string{"latest-check", bad, pinnedTag}},
		{"latest-check on a Hub FROM, other tag", []string{"latest-check", bad, "v2.198.0"}},
		{"tag without a file", []string{"tag"}},
		{"latest-check without a latest tag", []string{"latest-check", good}},
		{"no subcommand", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stdout, stderr, code := runIdppin(t, c.args...)
			if code != 2 {
				t.Errorf("exit %d, want 2; stdout=%q stderr=%q", code, stdout, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if stderr == "" {
				t.Error("stderr is empty; a refusal must say why")
			}
		})
	}
}

// d3Env is the committed ENV block the plan's D3, D8, D19 and D20 name, one value each.
var d3Env = map[string]string{
	"GOTRUE_DB_DRIVER":                        "postgres",
	"GOTRUE_DB_MAX_POOL_SIZE":                 "10",
	"GOTRUE_JWT_AUD":                          "authenticated",
	"GOTRUE_JWT_DEFAULT_GROUP_NAME":           "authenticated",
	"GOTRUE_JWT_EXP":                          "3600",
	"GOTRUE_DISABLE_SIGNUP":                   "true",
	"GOTRUE_MAILER_AUTOCONFIRM":               "false",
	"GOTRUE_HOOK_CUSTOM_ACCESS_TOKEN_ENABLED": "true",
	"GOTRUE_HOOK_CUSTOM_ACCESS_TOKEN_URI":     "pg-functions://postgres/public/custom_access_token_hook",
	"GOTRUE_SMTP_HOST":                        "smtp.resend.com",
	"GOTRUE_SMTP_PORT":                        "465",
	"GOTRUE_SMTP_USER":                        "resend",
	"GOTRUE_SMTP_ADMIN_EMAIL":                 "no-reply@ascomply.com",
	"GOTRUE_SMTP_SENDER_NAME":                 "ASComply",
	"GOTRUE_LOG_LEVEL":                        "info",
}

var envPairRE = regexp.MustCompile(`^([A-Z_][A-Z0-9_]*)=(\S*)$`)

// dockerfileEnv returns every ENV name=value in a comment-free Dockerfile, continuations joined.
func dockerfileEnv(t *testing.T, body string) map[string]string {
	t.Helper()
	var code []string
	for _, l := range strings.Split(body, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "#") {
			code = append(code, l)
		}
	}
	env := map[string]string{}
	for _, instr := range strings.Split(strings.ReplaceAll(strings.Join(code, "\n"), "\\\n", " "), "\n") {
		fields := strings.Fields(instr)
		if len(fields) == 0 || !strings.EqualFold(fields[0], "ENV") {
			continue
		}
		for _, f := range fields[1:] {
			m := envPairRE.FindStringSubmatch(f)
			if m == nil {
				t.Fatalf("ENV token %q is not NAME=value; the scan cannot read it", f)
			}
			if _, dup := env[m[1]]; dup {
				t.Errorf("ENV %s is set twice", m[1])
			}
			env[m[1]] = m[2]
		}
	}
	return env
}

func TestDockerfileCommitsTheD3EnvBlockAndNoSecret(t *testing.T) {
	raw, err := os.ReadFile(committedDockerfile)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	env := dockerfileEnv(t, body)
	if len(env) == 0 {
		t.Fatal("no ENV in the committed Dockerfile; the scan is broken")
	}
	if !maps.Equal(env, d3Env) {
		names := slices.Sorted(maps.Keys(env))
		for _, n := range names {
			if want, ok := d3Env[n]; !ok {
				t.Errorf("ENV %s=%s is not in D3; a secret or per-environment value belongs in Railway", n, env[n])
			} else if env[n] != want {
				t.Errorf("ENV %s=%s, want %s", n, env[n], want)
			}
		}
		for n := range d3Env {
			if _, ok := env[n]; !ok {
				t.Errorf("ENV %s is missing", n)
			}
		}
	}
	for _, secret := range []string{"GOTRUE_JWT_KEYS", "GOTRUE_JWT_SECRET", "GOTRUE_SMTP_PASS", "DATABASE_URL", "API_EXTERNAL_URL", "GOTRUE_JWT_ISSUER"} {
		if strings.Contains(body, secret) {
			t.Errorf("the Dockerfile names %s; it is a Railway variable, never baked into the image", secret)
		}
	}
	if strings.Contains(body, "# syntax=") {
		t.Error("the Dockerfile carries a # syntax= directive")
	}
}
