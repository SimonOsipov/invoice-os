// railway_env_mail_templates_test.go pins railway-env.sh check-mail-templates (D11) against the scripted
// Railway of railway_env_auth_test.go. Template URLs are plain-HTTP httptest servers with no <img>
// (QA F7): the built prenv cannot trust an httptest TLS certificate.
package main

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	mailTemplateVar = "GOTRUE_MAILER_TEMPLATES_CONFIRMATION"
	mailSiteURLVar  = "GOTRUE_SITE_URL"
	mailUsage       = "usage: railway-env.sh <assert-project-settings"
)

var mailNoticeRE = regexp.MustCompile(`GOTRUE_MAILER_TEMPLATES`)

// newMailTemplatesShim serves auth's variables from vars.
func newMailTemplatesShim(t *testing.T, vars map[string]string) authShim {
	t.Helper()
	settle := forkSettle(`{"node":{"serviceId":"` + authForkAuthID + `","serviceName":"auth"}}`)
	return newAuthShim(t, map[string]string{"settle": settle}, map[string]map[string]string{authForkAuthID: vars})
}

// runCheckMailTemplates runs the command with an account token; an unwired subcommand is a setup failure.
func runCheckMailTemplates(t *testing.T, s authShim, env string) (out string, code int) {
	t.Helper()
	stdout, stderr, code := s.run(t, forkExports(true, true, false), "check-mail-templates", env)
	if code == 2 && strings.Contains(stdout+stderr, mailUsage) {
		t.Fatal("railway-env.sh has no check-mail-templates subcommand: it printed its top-level usage line")
	}
	return stdout + stderr, code
}

func nonBlankLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// AC 6. Sibling variables that are not templates (a subject, a URL path) and an empty template are ignored.
func TestCheckMailTemplates_NoneSetPasses(t *testing.T) {
	s := newMailTemplatesShim(t, map[string]string{
		"GOTRUE_MAILER_SUBJECTS_CONFIRMATION": "Confirm your ASComply account",
		"GOTRUE_MAILER_URLPATHS_CONFIRMATION": "/auth/verify",
		"GOTRUE_MAILER_TEMPLATES_RECOVERY":    "",
		mailSiteURLVar:                        "https://www.ascomply.com",
	})
	out, code := runCheckMailTemplates(t, s, persistentEnvironmentID)

	if code != 0 {
		t.Errorf("exit %d, want 0; output = %q", code, out)
	}
	if n := opCount(t, s, "authVars"); n < 1 {
		t.Errorf("auth's variables were read %d times, want at least 1; a pass with no read proves nothing", n)
	}
	if lines := nonBlankLines(out); len(lines) != 1 || !mailNoticeRE.MatchString(lines[0]) {
		t.Errorf("want exactly one notice line naming GOTRUE_MAILER_TEMPLATES, got %q", lines)
	}
	if strings.Contains(out, "::error::") {
		t.Errorf("a pass printed an ::error:: line: %q", out)
	}
}

// AC 7.
func TestCheckMailTemplates_PassingURLPasses(t *testing.T) {
	srv := htmlServer(t, mailMinimalTemplate)
	s := newMailTemplatesShim(t, map[string]string{mailTemplateVar: srv.URL})
	out, code := runCheckMailTemplates(t, s, authForkEnvID)

	if code != 0 {
		t.Errorf("exit %d, want 0; output = %q", code, out)
	}
	if !strings.Contains(out, "ok "+srv.URL) {
		t.Errorf("output lacks `ok %s`: %q", srv.URL, out)
	}
	if strings.Contains(out, "::error::") {
		t.Errorf("a pass printed an ::error:: line: %q", out)
	}
}

// AC 7.
func TestCheckMailTemplates_SPAFailsNamingTheVariable(t *testing.T) {
	srv := htmlServer(t, mailSPA)
	s := newMailTemplatesShim(t, map[string]string{mailTemplateVar: srv.URL})
	out, code := runCheckMailTemplates(t, s, authForkEnvID)

	if code != 1 {
		t.Errorf("exit %d, want 1; output = %q", code, out)
	}
	e := errorLines(out)
	for _, needle := range []string{mailTemplateVar, srv.URL} {
		if !strings.Contains(e, needle) {
			t.Errorf("error lines lack %q: %q", needle, e)
		}
	}
	if !confURLRE.MatchString(e) {
		t.Errorf("error lines do not say the confirmation URL is missing: %q", e)
	}
}

// AC 7: GoTrue resolves a value that does not start with http against GOTRUE_SITE_URL (P5).
func TestCheckMailTemplates_RelativeValueFails(t *testing.T) {
	for _, value := range []string{"/emails/confirmation.html", "emails/confirmation.html"} {
		t.Run(value, func(t *testing.T) {
			s := newMailTemplatesShim(t, map[string]string{mailTemplateVar: value, mailSiteURLVar: "https://www.ascomply.com"})
			out, code := runCheckMailTemplates(t, s, authForkEnvID)

			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, out)
			}
			e := errorLines(out)
			for _, needle := range []string{mailTemplateVar, mailSiteURLVar} {
				if !strings.Contains(e, needle) {
					t.Errorf("error lines lack %q: %q", needle, e)
				}
			}
		})
	}
}

// AC 7: an unreadable variable map is a failure, never the "none set" pass.
func TestCheckMailTemplates_UnreadableVariablesFail(t *testing.T) {
	cases := map[string]func(t *testing.T, s authShim){
		"graphql error": func(t *testing.T, s authShim) {
			writeFile(t, filepath.Join(s.dir, "faults-authVars"), strings.Repeat("gqlerr ", 6))
		},
		"null variables": func(t *testing.T, s authShim) { s.bendRead(t, authForkAuthID, "null") },
	}
	for name, bend := range cases {
		t.Run(name, func(t *testing.T) {
			srv := htmlServer(t, mailMinimalTemplate)
			s := newMailTemplatesShim(t, map[string]string{mailTemplateVar: srv.URL})
			bend(t, s)
			out, code := runCheckMailTemplates(t, s, authForkEnvID)

			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, out)
			}
			if n := opCount(t, s, "authVars"); n < 1 {
				t.Errorf("auth's variables were read %d times, want at least 1", n)
			}
			if mailNoticeRE.MatchString(out) && !strings.Contains(out, "::error::") {
				t.Errorf("output is the none-set notice, not an error: %q", out)
			}
			if !regexp.MustCompile(`(?i)variables`).MatchString(errorLines(out)) {
				t.Errorf("error lines do not say the variables were unreadable: %q", errorLines(out))
			}
		})
	}
}
