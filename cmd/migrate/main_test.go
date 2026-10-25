package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	dbsql "github.com/SimonOsipov/invoice-os/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

const (
	grantSecret = "s3cret-xyz"
	grantMigDSN = "postgresql://invoice_migrator:m@h:5432/invoice_os?sslmode=disable"
)

type logLine struct{ Level, Msg, Class, Reason string }

func logLines(t *testing.T, buf *bytes.Buffer) []logLine {
	t.Helper()
	var out []logLine
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("log line %q: %v", l, err)
		}
		str := func(k string) string { s, _ := m[k].(string); return s }
		out = append(out, logLine{str("level"), str("msg"), str("class"), str("reason")})
	}
	return out
}

func TestRun_RefusesUnknownArguments(t *testing.T) {
	err := run(context.Background(), []string{"down"}, db.ProvisionConfig{}, nil, nil, nil, slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), `"down"`) {
		t.Fatalf("run(down) = %v, want an error naming the argument", err)
	}
}

func TestRun_BootstrapNeedsTheSuperuserURL(t *testing.T) {
	err := run(context.Background(), []string{"bootstrap"}, db.ProvisionConfig{}, nil, nil, nil, slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "DATABASE_SUPERUSER_URL") {
		t.Fatalf("run(bootstrap) with no superuser URL = %v, want an error naming DATABASE_SUPERUSER_URL", err)
	}
}

func TestGrantAccountStateRead_LogsOneOutcome(t *testing.T) {
	pgErr := &pgconn.PgError{Code: "42501", Message: "permission denied for " + grantSecret}
	cases := []struct {
		name      string
		password  string
		ok        bool
		err       error
		wantCalls int
		want      logLine
	}{
		{"granted", grantSecret, true, nil, 1, logLine{Level: "INFO", Msg: "migrate: account state read granted"}},
		{"auth.users not yet created", grantSecret, false, nil, 1, logLine{Level: "WARN", Msg: "migrate: account state read not granted", Class: "not_ready"}},
		{"postgres refuses", grantSecret, false, pgErr, 1, logLine{Level: "WARN", Msg: "migrate: account state read not granted", Class: "42501"}},
		{"connect fails", grantSecret, false, errors.New("dial " + grantSecret), 1, logLine{Level: "WARN", Msg: "migrate: account state read not granted", Class: "unknown"}},
		{"no password", "", true, nil, 0, logLine{Level: "WARN", Msg: "migrate: account state read not granted", Reason: "db: auth admin dsn: empty password"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			calls := 0
			var gotDSN string
			grant := func(_ context.Context, dsn string) (bool, error) {
				calls++
				gotDSN = dsn
				return c.ok, c.err
			}
			grantAccountStateRead(context.Background(), grantMigDSN, c.password, grant, slog.New(slog.NewJSONHandler(&buf, nil)))

			if calls != c.wantCalls {
				t.Fatalf("grant called %d time(s), want %d", calls, c.wantCalls)
			}
			if calls == 1 && !strings.HasPrefix(gotDSN, "postgresql://supabase_auth_admin:") {
				t.Errorf("grant dialled %q, want the supabase_auth_admin DSN", gotDSN)
			}
			lines := logLines(t, &buf)
			if len(lines) != 1 || lines[0] != c.want {
				t.Errorf("logged %+v, want exactly %+v", lines, c.want)
			}
			if strings.Contains(buf.String(), grantSecret) {
				t.Errorf("log carries the secret: %s", buf.String())
			}
		})
	}
}

// TestRun_MigratesThenGrants runs the job's default path against the test database. Production
// is off the seed allowlist, so it must not seed the demo tenants.
func TestRun_MigratesThenGrants(t *testing.T) {
	dsn := os.Getenv("DATABASE_MIGRATION_URL")
	if dsn == "" {
		t.Skip("DATABASE_MIGRATION_URL not set")
	}
	cfg := db.ProvisionConfig{
		Environment:  "production",
		MigrationDSN: dsn,
		Passwords:    db.RolePasswords{AuthAdmin: "auth_admin"},
		BootstrapFS:  dbsql.FS,
		MigrationsFS: migrations.FS,
		SeedFS:       dbsql.FS,
	}
	granted, seeded := 0, 0
	grant := func(context.Context, string) (bool, error) { granted++; return true, nil }
	seed := func(context.Context, *slog.Logger) { seeded++ }
	checked := 0
	check := func(context.Context, *slog.Logger) { checked++ }

	if err := run(context.Background(), nil, cfg, grant, seed, check, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("run: %v", err)
	}
	if granted != 1 || seeded != 0 {
		t.Errorf("production run: grant ran %d time(s), demo seeds %d, want 1 and 0", granted, seeded)
	}
	if checked != 0 {
		t.Errorf("production run: purge check ran %d time(s), want 0", checked)
	}
}

// TestRun_SeedsTheDemoTenantsOnAnAllowlistedEnvironment: the other half of the gate above.
func TestRun_SeedsTheDemoTenantsOnAnAllowlistedEnvironment(t *testing.T) {
	migDSN, superDSN := os.Getenv("DATABASE_MIGRATION_URL"), os.Getenv("DATABASE_SUPERUSER_URL")
	if migDSN == "" || superDSN == "" {
		t.Skip("DATABASE_MIGRATION_URL or DATABASE_SUPERUSER_URL not set")
	}
	cfg := db.ProvisionConfig{
		Environment:   "development",
		BootstrapFlag: "true",
		SuperuserDSN:  superDSN,
		MigrationDSN:  migDSN,
		Passwords: db.RolePasswords{
			Migrator: os.Getenv("MIGRATOR_PASSWORD"), App: os.Getenv("APP_PASSWORD"),
			Reader: os.Getenv("READER_PASSWORD"), AuthAdmin: os.Getenv("AUTH_ADMIN_PASSWORD"),
			Gateway: os.Getenv("GATEWAY_PASSWORD"),
		},
		BootstrapFS:  dbsql.FS,
		MigrationsFS: migrations.FS,
		SeedFS:       dbsql.FS,
	}
	seeded := 0
	grant := func(context.Context, string) (bool, error) { return true, nil }
	seed := func(context.Context, *slog.Logger) { seeded++ }
	checked := 0
	check := func(context.Context, *slog.Logger) { checked++ }

	if err := run(context.Background(), nil, cfg, grant, seed, check, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("run: %v", err)
	}
	if seeded != 1 {
		t.Errorf("demo seeds ran %d time(s) on development, want 1", seeded)
	}
	if checked != 0 {
		t.Errorf("purge check ran %d time(s) with an empty DEPLOY_ENV, want 0", checked)
	}
}

func TestPurgeCheckEnabled_OnlyOnAPreviewBootstrapRun(t *testing.T) {
	cases := []struct {
		env, flag, deploy string
		want              bool
	}{
		{"development", "true", "pr-12", true},
		{"pr-12", "true", "pr-12", true},
		{"development", "true", "invoice-os-pr-12", true},
		{"development", "true", "", false},
		{"development", "true", "production", false},
		{"development", "true", "PR-12", false},
		{"development", "false", "pr-12", false},
		{"development", "TRUE", "pr-12", false},
		{"production", "true", "pr-12", false},
		{"staging", "true", "pr-12", false},
	}
	for _, c := range cases {
		cfg := db.ProvisionConfig{Environment: c.env, BootstrapFlag: c.flag, DeployEnv: c.deploy}
		if got := purgeCheckEnabled(cfg); got != c.want {
			t.Errorf("purgeCheckEnabled(ENVIRONMENT=%q, GATEWAY_DB_BOOTSTRAP=%q, DEPLOY_ENV=%q) = %v, want %v",
				c.env, c.flag, c.deploy, got, c.want)
		}
	}
}

func TestPurgeCheckStep_CallsTheCheckOnlyWhenTheGateIsOn(t *testing.T) {
	cases := []struct {
		name, env, flag, deploy string
		want                    int
	}{
		{"gate on", "development", "true", "pr-12", 1},
		{"no deploy env", "development", "true", "", 0},
		{"grant run", "development", "false", "pr-12", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			check := func(context.Context, *slog.Logger) { calls++ }
			cfg := db.ProvisionConfig{Environment: c.env, BootstrapFlag: c.flag, DeployEnv: c.deploy}
			purgeCheckStep(context.Background(), cfg, check, slog.New(slog.DiscardHandler))
			if calls != c.want {
				t.Errorf("check called %d time(s), want %d", calls, c.want)
			}
		})
	}
}
