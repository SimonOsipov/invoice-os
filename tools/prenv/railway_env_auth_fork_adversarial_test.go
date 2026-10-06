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

// A missing or duplicated service name refuses before any write and names the service.
func TestSetForkAuthSite_MissingInstanceRefusesBeforeAnyWrite(t *testing.T) {
	gw := `{"node":{"serviceId":"` + authForkGatewayID + `","serviceName":"gateway"}}`
	auth := `{"node":{"serviceId":"` + authForkAuthID + `","serviceName":"auth"}}`
	siteShim := func(settle string) func(t *testing.T) authShim {
		return func(t *testing.T) authShim {
			resp := forkAuthRailway()
			resp["settle"] = forkSettle(settle)
			return newAuthShim(t, resp, forkAuthStores(freshJWK(t)))
		}
	}
	passShim := func(skip string, extra ...string) func(t *testing.T) authShim {
		return func(t *testing.T) authShim {
			settle := gtSettle(t, skip)
			for _, e := range extra {
				settle = strings.Replace(settle, `"edges":[`, `"edges":[`+e+`,`, 1)
			}
			return newPassShim(t, nil, map[string]string{"settle": settle})
		}
	}
	dup := func(name string) string {
		return `{"node":{"serviceId":"svc-` + name + `-dup","serviceName":"` + name + `"}}`
	}
	afterShim := func(skip string, extra ...string) func(t *testing.T) authShim {
		return func(t *testing.T) authShim { return afterShimIn(t, skip, extra...) }
	}
	urls := afterArgs()[1:]
	for _, c := range []struct {
		name, sub, service, says string
		args                     []string
		mk                       func(t *testing.T) authShim
	}{
		{"set-fork-auth-site without gateway", "set-fork-auth-site", "gateway", "AUTH_SITE_URL was NOT set", []string{forkSiteURL}, siteShim(auth)},
		{"set-fork-auth-site without auth", "set-fork-auth-site", "auth", "GOTRUE_SITE_URL was NOT set", []string{forkSiteURL}, siteShim(gw)},
		{"fork-vars-before-urls without auth", passSub, "auth", "", nil, passShim("auth")},
		{"fork-vars-before-urls without gateway", passSub, "gateway", "", nil, passShim("gateway")},
		{"fork-vars-before-urls with two auth", passSub, "auth", "", nil, passShim("", dup("auth"))},
		{"fork-vars-before-urls with two gateway", passSub, "gateway", "", nil, passShim("", dup("gateway"))},
		{"fork-vars-after-urls without auth", afterSub, "auth", "", urls, afterShim("auth")},
		{"fork-vars-after-urls without gateway", afterSub, "gateway", "", urls, afterShim("gateway")},
		{"fork-vars-after-urls without app", afterSub, "app", "", urls, afterShim("app")},
		{"fork-vars-after-urls without ops-console", afterSub, "ops-console", "", urls, afterShim("ops-console")},
		{"fork-vars-after-urls without submission", afterSub, "submission", "", urls, afterShim("submission")},
		{"fork-vars-after-urls without docling", afterSub, "docling", "", urls, afterShim("docling")},
		{"fork-vars-after-urls with two landing", afterSub, "landing", "", urls, afterShim("", dup("landing"))},
		{"reconcile-urls without support-console", "reconcile-urls", "support-console", "", urls, afterShim("support-console")},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := c.mk(t)
			out, code := forkAuthRun(t, s, c.sub, append([]string{authForkEnvID}, c.args...)...)
			if code != 1 {
				t.Errorf("exit %d, want 1; output = %q", code, clip(out))
			}
			errs := errorLines(out)
			if !strings.Contains(errs, "'"+c.service+"'") || !strings.Contains(errs, c.says) {
				t.Errorf("error lines %q do not name '%s' and carry %q", errs, c.service, c.says)
			}
			if !slices.Contains(operations(s.calls(t)), "settle") {
				t.Fatal("control: the run never listed service instances")
			}
			if m := s.mutations(t); len(m) != 0 {
				t.Errorf("a bad %s instance still wrote %v", c.service, m)
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
	s := runForkAuthOnEmptyFork(t)
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
		mk   func(t *testing.T) authShim
	}{
		{"set-fork-auth", []string{authForkEnvID}, newForkSiteShim},
		{"set-fork-auth-site", []string{authForkEnvID, forkSiteURL}, newForkSiteShim},
		{passSub, []string{authForkEnvID}, func(t *testing.T) authShim { return newPassShim(t, nil, nil) }},
		{afterSub, append([]string{authForkEnvID}, afterArgs()[1:]...), func(t *testing.T) authShim { return afterShimIn(t, "") }},
	} {
		t.Run(c.sub, func(t *testing.T) {
			s := c.mk(t)
			if out, code := forkAuthRun(t, s, c.sub, c.args...); code != 0 {
				t.Fatalf("exit %d, want 0; output = %q", code, clip(out))
			}
			ws := collectionWrites(t, s)
			if len(ws) == 0 {
				t.Fatal("control: no variableCollectionUpsert was sent")
			}
			for _, w := range ws {
				if w.SkipDeploys != true {
					t.Errorf("the %v write has skipDeploys %v, want true", w.Service, w.SkipDeploys)
				}
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
		{"reconcile-urls", afterArgs()[1:]},
		{afterSub, afterArgs()[1:]},
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
