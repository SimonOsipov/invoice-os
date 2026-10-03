package platform

import (
	"os"
	"testing"
	"time"
)

func TestLoadConfigDefaults(t *testing.T) {
	for _, k := range []string{"PORT", "ENVIRONMENT", "LOG_LEVEL", "SENTRY_DSN", "SHUTDOWN_TIMEOUT"} {
		t.Setenv(k, "")
	}
	cfg, err := LoadConfig("tenancy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Service != "tenancy" {
		t.Errorf("Service = %q, want tenancy", cfg.Service)
	}
	if cfg.Port != 8080 {
		t.Errorf("Port = %d, want 8080", cfg.Port)
	}
	if cfg.Environment != "development" {
		t.Errorf("Environment = %q, want development", cfg.Environment)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", cfg.LogLevel)
	}
	if cfg.SentryDSN != "" {
		t.Errorf("SentryDSN = %q, want empty", cfg.SentryDSN)
	}
	if cfg.ShutdownTimeout != 10*time.Second {
		t.Errorf("ShutdownTimeout = %s, want 10s", cfg.ShutdownTimeout)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	t.Setenv("PORT", "9090")
	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("SENTRY_DSN", "https://key@example.com/1")
	t.Setenv("SHUTDOWN_TIMEOUT", "30s")
	cfg, err := LoadConfig("invoice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != 9090 {
		t.Errorf("Port = %d, want 9090", cfg.Port)
	}
	if cfg.Environment != "production" {
		t.Errorf("Environment = %q, want production", cfg.Environment)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.LogLevel)
	}
	if cfg.SentryDSN != "https://key@example.com/1" {
		t.Errorf("SentryDSN = %q", cfg.SentryDSN)
	}
	if cfg.ShutdownTimeout != 30*time.Second {
		t.Errorf("ShutdownTimeout = %s, want 30s", cfg.ShutdownTimeout)
	}
}

func TestLoadConfigEmptyService(t *testing.T) {
	if _, err := LoadConfig(""); err == nil {
		t.Fatal("expected error for empty service name")
	}
}

func TestLoadConfigInvalidPort(t *testing.T) {
	t.Setenv("PORT", "not-a-number")
	if _, err := LoadConfig("svc"); err == nil {
		t.Fatal("expected error for invalid PORT")
	}
}

func TestLoadConfigInvalidDuration(t *testing.T) {
	t.Setenv("SHUTDOWN_TIMEOUT", "nope")
	if _, err := LoadConfig("svc"); err == nil {
		t.Fatal("expected error for invalid SHUTDOWN_TIMEOUT")
	}
}

const (
	fakeStampedSHA = "5e975e718251c892c7cbfd3602bf6aa009f37ce5"
	fakeRailwaySHA = "d0e09998b867ee781c56969b28f9497a2c2f1595"
)

// Production runs ENVIRONMENT=development; only the Sentry label follows Railway.
func TestLoadConfig_SentryEnvironmentFromRailway(t *testing.T) {
	for _, c := range []struct{ name, railway, environment string }{
		{"production_label", "production", "development"},
		{"railway_wins_over_environment_production", "invoice-os-pr-283", "production"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("ENVIRONMENT", c.environment)
			t.Setenv("RAILWAY_ENVIRONMENT_NAME", c.railway)
			cfg, err := LoadConfig("svc")
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if cfg.SentryEnvironment != c.railway {
				t.Errorf("SentryEnvironment = %q, want %q", cfg.SentryEnvironment, c.railway)
			}
			if cfg.Environment != c.environment {
				t.Errorf("Environment = %q, want %q (gates stay on ENVIRONMENT)", cfg.Environment, c.environment)
			}
		})
	}
}

func TestLoadConfig_SentryEnvironmentFallsBack(t *testing.T) {
	for _, c := range []struct {
		name, environment, want string
		unset                   bool
	}{
		{"environment_set", "staging", "staging", false},
		{"both_empty", "", "development", false},
		{"railway_unset", "staging", "staging", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("RAILWAY_ENVIRONMENT_NAME", "")
			if c.unset {
				os.Unsetenv("RAILWAY_ENVIRONMENT_NAME") // t.Setenv above restores it
			}
			t.Setenv("ENVIRONMENT", c.environment)
			cfg, err := LoadConfig("svc")
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if cfg.SentryEnvironment != c.want {
				t.Errorf("SentryEnvironment = %q, want %q", cfg.SentryEnvironment, c.want)
			}
		})
	}
}

func TestReleaseName(t *testing.T) {
	for _, c := range []struct{ name, build, railway, want string }{
		{"stamped", fakeStampedSHA, "", fakeStampedSHA},
		{"stamp_wins_over_railway", fakeStampedSHA, fakeRailwaySHA, fakeStampedSHA},
		{"unstamped_with_railway_sha", "dev", fakeRailwaySHA, "unstamped-" + fakeRailwaySHA},
		{"unstamped", "dev", "", "unstamped"},
		{"empty_build", "", "", "unstamped"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := releaseName(c.build, c.railway)
			if got != c.want {
				t.Errorf("releaseName(%q, %q) = %q, want %q", c.build, c.railway, got, c.want)
			}
			if got == "" || got == "dev" {
				t.Errorf("releaseName(%q, %q) = %q, want neither empty nor dev", c.build, c.railway, got)
			}
		})
	}
}

// setBuildSHA overrides the embedded stamp for one test.
func setBuildSHA(t *testing.T, sha string) {
	t.Helper()
	prev := BuildSHA
	BuildSHA = sha
	t.Cleanup(func() { BuildSHA = prev })
}

func TestLoadConfig_ReleaseFromStampedBuild(t *testing.T) {
	setBuildSHA(t, fakeStampedSHA)
	t.Setenv("RAILWAY_GIT_COMMIT_SHA", fakeRailwaySHA)
	cfg, err := LoadConfig("svc")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Release != fakeStampedSHA {
		t.Errorf("Release = %q, want the stamped %q", cfg.Release, fakeStampedSHA)
	}
}

func TestLoadConfig_SentryTestEventIsExactTrue(t *testing.T) {
	for _, c := range []struct {
		name  string
		value *string
		want  bool
	}{
		{"unset", nil, false},
		{"empty", ptr(""), false},
		{"false", ptr("false"), false},
		{"capital_True", ptr("True"), false},
		{"true", ptr("true"), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("SENTRY_TEST_EVENT", "")
			if c.value == nil {
				os.Unsetenv("SENTRY_TEST_EVENT")
			} else {
				t.Setenv("SENTRY_TEST_EVENT", *c.value)
			}
			cfg, err := LoadConfig("svc")
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if cfg.SentryTestEvent != c.want {
				t.Errorf("SentryTestEvent = %v, want %v", cfg.SentryTestEvent, c.want)
			}
		})
	}
}

func ptr(s string) *string { return &s }
