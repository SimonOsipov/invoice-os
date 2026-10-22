// Package platform is the shared service kit every ASComply backend binary
// is built on: environment configuration, structured logging, an HTTP server
// with graceful shutdown, health/readiness probes, panic recovery, a Sentry
// hook, and the standard middleware chain. Observability is baked in
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
	Service           string        // logical service name, e.g. "tenancy" (set in code, not env)
	Environment       string        // deployment environment: development, production, ...
	SentryEnvironment string        // Sentry's environment label
	Release           string        // Sentry release
	Port              int           // HTTP listen port
	LogLevel          string        // debug, info, warn, error
	SentryDSN         string        // empty disables Sentry
	SentryTestEvent   bool          // send one test event at boot; only the exact value "true"
	ShutdownTimeout   time.Duration // graceful-shutdown grace period
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
	deployEnv := DeployEnv()
	// Cloud Run sets K_SERVICE; a hosted service must never fall back to Local.
	if deployEnv == "" && os.Getenv("K_SERVICE") != "" {
		return Config{}, fmt.Errorf("platform: DEPLOY_ENV is required on Cloud Run")
	}
	environment := envString("ENVIRONMENT", "development")
	return Config{
		Service:     service,
		Environment: environment,
		// Production runs ENVIRONMENT=development, so only Sentry's label follows DEPLOY_ENV.
		SentryEnvironment: firstNonEmpty(deployEnv, environment),
		Release:           releaseName(BuildSHA, firstNonEmpty(os.Getenv("BUILD_SHA"), os.Getenv("RAILWAY_GIT_COMMIT_SHA"))),
		Port:              port,
		LogLevel:          envString("LOG_LEVEL", "info"),
		SentryDSN:         envString("SENTRY_DSN", ""),
		SentryTestEvent:   os.Getenv("SENTRY_TEST_EVENT") == "true",
		ShutdownTimeout:   shutdown,
	}, nil
}

// releaseName is the Sentry release for a build. The "unstamped" prefix keeps a
// platform rebuild apart from the CI build of the same commit.
func releaseName(buildSHA, platformSHA string) string {
	switch {
	case buildSHA != "" && buildSHA != "dev":
		return buildSHA
	case platformSHA != "":
		return "unstamped-" + platformSHA
	default:
		return "unstamped"
	}
}

// ceiling: second value is the Railway fallback, delete with Railway in GKE-12.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
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
