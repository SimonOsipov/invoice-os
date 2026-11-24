package erp

import (
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestModeFromEnv_EphemeralPosturesAreAlwaysFake(t *testing.T) {
	for _, p := range []platform.PostureKind{platform.PosturePreview, platform.PostureNightly} {
		for _, v := range []string{"", "false", "banana"} {
			get := func(k string) string {
				if k == "ERP_FAKE" {
					t.Errorf("%s read ERP_FAKE", p)
				}
				return v
			}
			if m, err := ModeFromEnv(get, p); err != nil || m != ModeFake {
				t.Errorf("%s %q: %v, %v", p, v, m, err)
			}
		}
	}
}

func TestModeFromEnv_HostedWithTheFlagRefuses(t *testing.T) {
	for _, v := range []string{"true", "1", "TRUE"} {
		_, err := ModeFromEnv(env(map[string]string{"ERP_FAKE": v}), platform.PostureHosted)
		if err == nil || !strings.Contains(err.Error(), "ERP_FAKE") || strings.Contains(err.Error(), v) {
			t.Errorf("%q: %v", v, err)
		}
	}
}

func TestModeFromEnv_HostedWithoutTheFlagIsOff(t *testing.T) {
	for _, v := range []string{"", "false"} {
		if m, err := ModeFromEnv(env(map[string]string{"ERP_FAKE": v}), platform.PostureHosted); err != nil || m != ModeOff {
			t.Errorf("%q: %v, %v", v, m, err)
		}
	}
}

func TestModeFromEnv_LocalFollowsTheFlag(t *testing.T) {
	want := map[string]Mode{"true": ModeFake, "": ModeOff, "false": ModeOff}
	for v, w := range want {
		if m, err := ModeFromEnv(env(map[string]string{"ERP_FAKE": v}), platform.PostureLocal); err != nil || m != w {
			t.Errorf("%q: %v, %v", v, m, err)
		}
	}
}

func TestModeFromEnv_UnreadableFlagRefuses(t *testing.T) {
	for _, p := range []platform.PostureKind{platform.PostureHosted, platform.PostureLocal} {
		_, err := ModeFromEnv(env(map[string]string{"ERP_FAKE": "banana"}), p)
		if err == nil || !strings.Contains(err.Error(), "not a boolean") || strings.Contains(err.Error(), "banana") {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func TestRegistryFromEnv_SystemsPerDeployEnv(t *testing.T) {
	for _, d := range []string{"pr-12", "nightly"} {
		r, err := RegistryFromEnv(env(map[string]string{"DEPLOY_ENV": d, "ERP_REDIRECT_URL": "https://gw.example/erp/callback"}))
		if err != nil || strings.Join(r.Systems(), ",") != "fake" {
			t.Errorf("%s: %v, %v", d, r.Systems(), err)
		}
	}
	r, err := RegistryFromEnv(env(map[string]string{"DEPLOY_ENV": "production"}))
	if err != nil || r.Systems() == nil || len(r.Systems()) != 0 {
		t.Errorf("production: %v, %v", r.Systems(), err)
	}
	if _, err := RegistryFromEnv(env(map[string]string{"DEPLOY_ENV": "production", "ERP_FAKE": "true"})); err == nil {
		t.Error("production with ERP_FAKE=true accepted")
	}
}
func TestRegistryFromEnv_RedirectURL(t *testing.T) {
	const good = "https://gw.example/erp/callback"
	for _, c := range []struct {
		name string
		env  map[string]string
		ok   bool
	}{
		{"preview unset", map[string]string{"DEPLOY_ENV": "pr-1"}, false},
		{"preview relative", map[string]string{"DEPLOY_ENV": "pr-1", "ERP_REDIRECT_URL": "/erp/callback"}, false},
		{"preview https without a host", map[string]string{"DEPLOY_ENV": "pr-1", "ERP_REDIRECT_URL": "https:///erp/callback"}, false},
		{"preview http", map[string]string{"DEPLOY_ENV": "pr-1", "ERP_REDIRECT_URL": "http://x/erp/callback"}, false},
		{"preview other scheme", map[string]string{"DEPLOY_ENV": "pr-1", "ERP_REDIRECT_URL": "ftp://x/erp/callback"}, false},
		{"nightly http", map[string]string{"DEPLOY_ENV": "nightly", "ERP_REDIRECT_URL": "http://x/erp/callback"}, false},
		{"preview https", map[string]string{"DEPLOY_ENV": "pr-1", "ERP_REDIRECT_URL": good}, true},
		{"local unset", map[string]string{"ERP_FAKE": "true"}, false},
		{"local http", map[string]string{"ERP_FAKE": "true", "ERP_REDIRECT_URL": "http://localhost:8080/erp/callback"}, true},
	} {
		r, err := RegistryFromEnv(env(c.env))
		if c.ok != (err == nil) {
			t.Errorf("%s: %v", c.name, err)
		}
		if c.ok && len(r) != 1 {
			t.Errorf("%s: systems %v", c.name, r.Systems())
		}
		if err != nil {
			if !strings.Contains(err.Error(), "ERP_REDIRECT_URL") || strings.Contains(err.Error(), "erp/callback") {
				t.Errorf("%s: error %q must name the variable and not the value", c.name, err)
			}
		}
	}
	if _, err := RegistryFromEnv(env(map[string]string{"DEPLOY_ENV": "pr-1"})); err == nil || !strings.Contains(err.Error(), "is not set") {
		t.Errorf("an unset redirect: %v, want a not-set error", err)
	}
	asked := false
	get := func(k string) string {
		if k == "ERP_REDIRECT_URL" {
			asked = true
		}
		return map[string]string{"DEPLOY_ENV": "production"}[k]
	}
	if _, err := RegistryFromEnv(get); err != nil || asked {
		t.Errorf("hosted off: %v, asked = %v", err, asked)
	}
}
