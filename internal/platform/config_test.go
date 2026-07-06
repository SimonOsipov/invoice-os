package platform

import (
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
