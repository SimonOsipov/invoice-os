package main

import (
	"slices"
	"strings"
	"testing"
)

func forkAuthRun(t *testing.T, s authShim, sub string, args ...string) (string, int) {
	t.Helper()
	stdout, stderr, code := s.run(t, forkAuthExports(), sub, args...)
	return stdout + stderr, code
}

func TestSetForkAuthSite_MissingInstanceRefusesBeforeAnyWrite(t *testing.T) {
	gw := `{"node":{"serviceId":"` + authForkGatewayID + `","serviceName":"gateway"}}`
	auth := `{"node":{"serviceId":"` + authForkAuthID + `","serviceName":"auth"}}`
	for _, c := range []struct{ missing, variable, present string }{
		{"gateway", "AUTH_SITE_URL", auth},
		{"auth", "GOTRUE_SITE_URL", gw},
	} {
		t.Run(c.missing, func(t *testing.T) {
			resp := forkAuthRailway()
			resp["settle"] = forkSettle(c.present)
			s := newAuthShim(t, resp, forkAuthStores(freshJWK(t)))
			out, code := forkAuthRun(t, s, "set-fork-auth-site", authForkEnvID, forkSiteURL)
			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, out)
			}
			errs := errorLines(out)
			if !strings.Contains(errs, "'"+c.missing+"'") || !strings.Contains(errs, c.variable+" was NOT set") {
				t.Errorf("error lines %q do not name the missing '%s' and %s", errs, c.missing, c.variable)
			}
			if !slices.Contains(operations(s.calls(t)), "settle") {
				t.Fatal("control: the run never listed service instances")
			}
			if m := s.mutations(t); len(m) != 0 {
				t.Errorf("a missing %s instance still wrote %v", c.missing, m)
			}
		})
	}
}

func TestSetForkAuthSite_URLArgument(t *testing.T) {
	t.Run("a bare https:// is refused", func(t *testing.T) {
		s := newForkSiteShim(t)
		out, code := forkAuthRun(t, s, "set-fork-auth-site", authForkEnvID, "https://")
		if code != 1 || !strings.Contains(errorLines(out), "https://") {
			t.Errorf("exit %d and error lines %q, want exit 1 asking for an https:// URL", code, errorLines(out))
		}
		if m := s.mutations(t); len(m) != 0 {
			t.Errorf("the refusal wrote %v", m)
		}
	})
	// Only the scheme is checked; a malformed host reaches the gateway, which refuses it at boot.
	for name, url := range map[string]string{"trailing slash": forkSiteURL + "/", "malformed host": "https://not a url"} {
		t.Run(name+" is written verbatim", func(t *testing.T) {
			s := newForkSiteShim(t)
			if out, code := forkAuthRun(t, s, "set-fork-auth-site", authForkEnvID, url); code != 0 {
				t.Fatalf("exit %d, want 0; output = %q", code, out)
			}
			want := []authUpsert{{authForkAuthID, "GOTRUE_SITE_URL", url}, {authForkGatewayID, "AUTH_SITE_URL", url}}
			if got := s.upserts(t); !slices.Equal(got, want) {
				t.Errorf("upserts = %v, want %v", got, want)
			}
		})
	}
}

// Adding GOTRUE_DISABLE_SIGNUP must not drop or add any other fork variable.
func TestSetForkAuth_WritesTheCompleteSet(t *testing.T) {
	s, _ := runForkAuthOK(t, freshJWK(t))
	got := map[string][]string{}
	for _, u := range s.upserts(t) {
		got[u.Service] = append(got[u.Service], u.Name)
	}
	want := map[string][]string{
		authForkAuthID: {"API_EXTERNAL_URL", "DATABASE_URL", "GOTRUE_DISABLE_SIGNUP", "GOTRUE_JWT_ISSUER",
			"GOTRUE_JWT_KEYS", "GOTRUE_JWT_SECRET", "GOTRUE_MAILER_AUTOCONFIRM", "GOTRUE_SMTP_HOST", "GOTRUE_SMTP_PASS", "PORT"},
		authForkGatewayID: {"AUTH_ADDITIONAL_ISSUERS", "AUTH_ADMIN_PASSWORD", "AUTH_ISSUER", "AUTH_JWKS_URL", "AUTH_URL"},
	}
	if len(got) != len(want) {
		t.Errorf("set-fork-auth wrote services %v, want exactly auth and gateway", got)
	}
	for svc, names := range want {
		g := slices.Clone(got[svc])
		slices.Sort(g)
		if !slices.Equal(g, names) {
			t.Errorf("%s upserts = %v, want %v", svc, g, names)
		}
	}
}

func TestForkAuthWritesSkipDeploys(t *testing.T) {
	for _, c := range []struct {
		sub  string
		args []string
	}{
		{"set-fork-auth", []string{authForkEnvID}},
		{"set-fork-auth-site", []string{authForkEnvID, forkSiteURL}},
	} {
		t.Run(c.sub, func(t *testing.T) {
			s := newForkSiteShim(t)
			if out, code := forkAuthRun(t, s, c.sub, c.args...); code != 0 {
				t.Fatalf("exit %d, want 0; output = %q", code, out)
			}
			n := 0
			for _, call := range s.calls(t) {
				if !strings.Contains(call.Query, "variableUpsert(") {
					continue
				}
				n++
				in, _ := call.Variables["input"].(map[string]any)
				if in["skipDeploys"] != true {
					t.Errorf("%v.%v upsert has skipDeploys %v, want true", in["serviceId"], in["name"], in["skipDeploys"])
				}
			}
			if n == 0 {
				t.Fatal("control: no variableUpsert was sent")
			}
		})
	}
}

// Both guards precede service resolution, so a refused run never learns a service id.
func TestForkAuthRefusalsPrecedeServiceResolution(t *testing.T) {
	for _, c := range []struct {
		sub  string
		args []string
	}{
		{"set-fork-auth", nil},
		{"set-fork-auth-site", []string{forkSiteURL}},
	} {
		t.Run(c.sub, func(t *testing.T) {
			resp := forkAuthRailway()
			resp["envList"] = authEnvList(false)
			s := newAuthShim(t, resp, forkAuthStores(freshJWK(t)))
			out, code := forkAuthRun(t, s, c.sub, append([]string{authStaleEnvID}, c.args...)...)
			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, out)
			}
			ops := operations(s.calls(t))
			if !slices.Contains(ops, "envList") {
				t.Fatalf("control: Railway calls = %v; the ephemeral check never ran", ops)
			}
			if slices.Contains(ops, "settle") {
				t.Errorf("Railway calls = %v; a non-ephemeral environment reached service resolution", ops)
			}
		})
	}
}
