package erp

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform"
)

var intuitNames = []string{"INTUIT_CLIENT_ID", "INTUIT_CLIENT_SECRET", "INTUIT_WEBHOOK_VERIFIER"}

// nightlyEnv is a complete nightly environment with runtime-built Intuit values.
func nightlyEnv() map[string]string {
	m := map[string]string{"DEPLOY_ENV": "nightly", "ERP_REDIRECT_URL": "https://gw.example/erp/callback"}
	for _, n := range intuitNames {
		m[n] = "v-" + uuid.NewString()
	}
	return m
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestModeFromEnv_NightlyIsSandboxWithoutReadingAFlag(t *testing.T) {
	for _, v := range []string{"", "false", "true", "banana"} {
		get := func(k string) string {
			if k == "ERP_FAKE" || k == "ERP_SANDBOX" {
				t.Errorf("nightly read %s", k)
			}
			return v
		}
		if m, err := ModeFromEnv(get, platform.PostureNightly); err != nil || m != ModeSandbox {
			t.Errorf("%q: %v, %v", v, m, err)
		}
	}
}

func TestModeFromEnv_PreviewIsAlwaysFake(t *testing.T) {
	for _, v := range []string{"", "false", "true", "banana"} {
		get := func(k string) string {
			if k == "ERP_FAKE" || k == "ERP_SANDBOX" {
				t.Errorf("preview read %s", k)
			}
			return v
		}
		if m, err := ModeFromEnv(get, platform.PosturePreview); err != nil || m != ModeFake {
			t.Errorf("%q: %v, %v", v, m, err)
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

func TestModeFromEnv_HostedWithTheSandboxFlagRefuses(t *testing.T) {
	for _, v := range []string{"true", "1", "TRUE"} {
		_, err := ModeFromEnv(env(map[string]string{"ERP_SANDBOX": v}), platform.PostureHosted)
		if err == nil || !strings.Contains(err.Error(), "ERP_SANDBOX") || strings.Contains(err.Error(), v) {
			t.Errorf("%q: %v", v, err)
		}
	}
}

func TestModeFromEnv_HostedWithoutTheFlagIsOff(t *testing.T) {
	for _, name := range []string{"ERP_FAKE", "ERP_SANDBOX"} {
		for _, v := range []string{"", "false"} {
			if m, err := ModeFromEnv(env(map[string]string{name: v}), platform.PostureHosted); err != nil || m != ModeOff {
				t.Errorf("%s %q: %v, %v", name, v, m, err)
			}
		}
	}
}

func TestModeFromEnv_LocalFollowsTheFlag(t *testing.T) {
	for _, c := range []struct {
		env  map[string]string
		want Mode
	}{
		{map[string]string{"ERP_FAKE": "true"}, ModeFake},
		{map[string]string{"ERP_FAKE": "false"}, ModeOff},
		{map[string]string{"ERP_SANDBOX": "true"}, ModeSandbox},
		{map[string]string{"ERP_SANDBOX": "true", "ERP_FAKE": "true"}, ModeSandbox},
		{map[string]string{"ERP_SANDBOX": "false", "ERP_FAKE": "true"}, ModeFake},
		{map[string]string{}, ModeOff},
	} {
		if m, err := ModeFromEnv(env(c.env), platform.PostureLocal); err != nil || m != c.want {
			t.Errorf("%v: %v, %v", c.env, m, err)
		}
	}
}

func TestModeFromEnv_UnreadableFlagRefuses(t *testing.T) {
	for _, name := range []string{"ERP_FAKE", "ERP_SANDBOX"} {
		for _, p := range []platform.PostureKind{platform.PostureHosted, platform.PostureLocal} {
			_, err := ModeFromEnv(env(map[string]string{name: "banana"}), p)
			if err == nil || !strings.Contains(err.Error(), name+" is not a boolean") || strings.Contains(err.Error(), "banana") {
				t.Errorf("%s %s: %v", name, p, err)
			}
		}
	}
}

func TestModeRunsFake(t *testing.T) {
	for m, want := range map[Mode]bool{ModeOff: false, ModeFake: true, ModeSandbox: true} {
		if m.RunsFake() != want {
			t.Errorf("%v.RunsFake() = %v, want %v", m, m.RunsFake(), want)
		}
	}
}

func TestRegistryFromEnv_SystemsPerDeployEnv(t *testing.T) {
	r, err := RegistryFromEnv(env(map[string]string{"DEPLOY_ENV": "pr-12", "ERP_REDIRECT_URL": "https://gw.example/erp/callback"}))
	if err != nil || strings.Join(r.Systems(), ",") != "fake" {
		t.Errorf("pr-12: %v, %v", r.Systems(), err)
	}
	r, err = RegistryFromEnv(env(nightlyEnv()))
	if err != nil || strings.Join(r.Systems(), ",") != "fake,quickbooks" {
		t.Errorf("nightly: %v, %v", r.Systems(), err)
	}
	r, err = RegistryFromEnv(env(map[string]string{"DEPLOY_ENV": "production"}))
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

func TestRegistryFromEnv_SandboxNeedsEachIntuitVariable(t *testing.T) {
	for _, missing := range intuitNames {
		m := nightlyEnv()
		delete(m, missing)
		_, err := RegistryFromEnv(env(m))
		if err == nil || !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "is not set") {
			t.Fatalf("%s unset: %v", missing, err)
		}
		for _, n := range intuitNames {
			if v, ok := m[n]; ok && strings.Contains(err.Error(), v) {
				t.Errorf("%s unset: error carries the value of %s", missing, n)
			}
		}
	}
}

func TestRegistryFromEnv_SandboxRefusesAPaddedIntuitValue(t *testing.T) {
	for _, padded := range intuitNames {
		m := nightlyEnv()
		m[padded] += "\n"
		_, err := RegistryFromEnv(env(m))
		if err == nil || !strings.Contains(err.Error(), padded) || !strings.Contains(err.Error(), "surrounding whitespace") {
			t.Fatalf("%s padded: %v", padded, err)
		}
		if strings.Contains(err.Error(), strings.TrimSpace(m[padded])) {
			t.Errorf("%s padded: error carries the value", padded)
		}
	}
}

func TestRegistryFromEnv_SandboxChecksTheRedirectFirst(t *testing.T) {
	_, err := RegistryFromEnv(env(map[string]string{"DEPLOY_ENV": "nightly"}))
	if err == nil || !strings.Contains(err.Error(), "ERP_REDIRECT_URL") {
		t.Errorf("got %v, want the redirect named first", err)
	}
}

func TestRegistryFromEnv_OnlySandboxReadsIntuit(t *testing.T) {
	for _, base := range []map[string]string{
		{"DEPLOY_ENV": "pr-1", "ERP_REDIRECT_URL": "https://gw.example/erp/callback"},
		{"DEPLOY_ENV": "production"},
		{"ERP_FAKE": "true", "ERP_REDIRECT_URL": "http://localhost:8080/erp/callback"},
	} {
		get := func(k string) string {
			if strings.HasPrefix(k, "INTUIT_") {
				t.Errorf("%v read %s", base, k)
			}
			return base[k]
		}
		if _, err := RegistryFromEnv(get); err != nil {
			t.Errorf("%v: %v", base, err)
		}
	}
}
