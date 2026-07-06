// Package platform is the shared service kit every FiscalBridge backend binary
// is built on: environment configuration, structured logging, an HTTP server
// with graceful shutdown, health/readiness probes, panic recovery, a Sentry
// error hook, and the standard middleware chain. Observability is baked in
// here by design — there is deliberately no "add observability later" step.
package platform

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds process-level configuration loaded from the environment. Every
// service shares this shape; service-specific configuration is layered on top
// by the owning service.
type Config struct {
	Service         string        // logical service name, e.g. "tenancy" (set in code, not env)
	Environment     string        // deployment environment: development, production, ...
	Port            int           // HTTP listen port
	LogLevel        string        // debug, info, warn, error
	SentryDSN       string        // empty disables Sentry
	ShutdownTimeout time.Duration // graceful-shutdown grace period
}

// LoadConfig reads configuration from the environment, applying defaults. The
// service name is passed in code rather than read from the environment so a
// misconfigured env can never mislabel a binary in logs or Sentry.
func LoadConfig(service string) (Config, error) {
	if service == "" {
		return Config{}, fmt.Errorf("platform: service name is required")
	}
	port, err := envInt("PORT", 8080)
	if err != nil {
		return Config{}, err
	}
	shutdown, err := envDuration("SHUTDOWN_TIMEOUT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	return Config{
		Service:         service,
		Environment:     envString("ENVIRONMENT", "development"),
		Port:            port,
		LogLevel:        envString("LOG_LEVEL", "info"),
		SentryDSN:       envString("SENTRY_DSN", ""),
		ShutdownTimeout: shutdown,
	}, nil
}

func envString(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("platform: invalid %s=%q: %w", key, v, err)
	}
	return n, nil
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("platform: invalid %s=%q: %w", key, v, err)
	}
	return d, nil
}
