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
		r, err := RegistryFromEnv(env(map[string]string{"DEPLOY_ENV": d}))
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
