// provision.go — the gateway boot-time provisioning sequence (M4-21-04,
// task-128): bootstrap → migrate → seed, gated by BootstrapEnabled, all fatal on
// error and all complete before the gateway's listener opens (so a green /healthz
// continues to mean "fully provisioned"). See db.go for the package doc and
// bootstrap.go for the underlying BootstrapEnabled/Bootstrap/Seed primitives this
// composes; MigrateUp (migrate.go) is the third leg, unchanged.
//
// cmd/gateway/main.go calls Provision with Environment set to the RAW
// os.Getenv("ENVIRONMENT") — never app.Config.Environment, which substitutes
// "development" for an unset var (internal/platform/config.go:44) and would
// silently re-open the fail-open hole BootstrapEnabled's allowlist exists to
// close (QA F1, task-128's crux requirement — see
// TestProvisionGuardReadsRawEnvironment and
// TestGatewayMainPassesRawEnvironmentToProvisioningGuard in provision_test.go).
package db

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ProvisionConfig bundles everything Provision needs to run the boot-time
// bootstrap→migrate→seed sequence.
//
// Environment MUST be the raw os.Getenv("ENVIRONMENT") value (see
// BootstrapEnabled's doc comment) — Provision applies no substitution of its
// own, so passing app.Config.Environment here would defeat the allowlist guard
// one layer up. That call-site contract is what
// TestGatewayMainPassesRawEnvironmentToProvisioningGuard pins directly against
// cmd/gateway/main.go's source.
type ProvisionConfig struct {
	Environment   string // raw os.Getenv("ENVIRONMENT")
	BootstrapFlag string // raw os.Getenv("GATEWAY_DB_BOOTSTRAP")

	SuperuserDSN string // DATABASE_SUPERUSER_URL; read once per gated step, never retained (QA F3)
	MigrationDSN string // DATABASE_MIGRATION_URL; always required, guard-independent (unchanged gateway-as-migrator behavior)

	Passwords    RolePasswords // fiscalbridge.*_password GUCs Bootstrap sets
	BootstrapFS  fs.FS         // db/bootstrap.sql (db.FS)
	MigrationsFS fs.FS         // embedded migrations (migrations.FS)
	SeedFS       fs.FS         // db/seed.dev.sql (db.FS)

	// ConnectWait bounds how long Provision waits for Postgres to accept its
	// FIRST connection before running the sequence. Zero (the default, and what
	// every non-gateway caller and every unit test uses) disables the wait
	// entirely and preserves the original fail-immediately behaviour.
	//
	// cmd/gateway/main.go sets it, because the gateway is the one caller that
	// boots against a Postgres that may not be serving yet. See waitForPostgres
	// for why that wait replaced an out-of-network probe (M4-22-08).
	ConnectWait time.Duration

	// Logger receives the wait's progress lines. Nil falls back to
	// slog.Default(), which platform.New has already pointed at the process's
	// JSON logger by the time the gateway calls Provision.
	Logger *slog.Logger
}

// unrenderedReference is the opening delimiter of a Railway variable reference.
// A DSN that still contains it was never rendered: the variable EXISTS on the
// service, so a bare non-empty check passes, but the value it points at did not
// resolve in this environment. Connecting with it fails with a URL-parse or DNS
// error that names neither the variable nor the real cause — which is exactly
// the blind failure this check exists to convert into a named one.
const unrenderedReference = "${{"

// postgresWaitInterval is the gap between connection attempts in
// waitForPostgres. Coarse on purpose: the thing being waited on is a container
// finishing initdb and opening its socket, measured in tens of seconds, so a
// tighter poll would only add log noise.
const postgresWaitInterval = 3 * time.Second

// validateDSN rejects a connection string that cannot possibly work, naming the
// ENVIRONMENT VARIABLE it came from rather than the value — the value is a
// credential, and the variable name is what the operator has to go fix.
func validateDSN(envVar, dsn string) error {
	if strings.TrimSpace(dsn) == "" {
		return fmt.Errorf("db: %s is empty — the gateway cannot reach Postgres without it", envVar)
	}
	if strings.Contains(dsn, unrenderedReference) {
		return fmt.Errorf("db: %s is an UNRENDERED variable reference — the variable is set on this service but the value it points at did not resolve in this environment", envVar)
	}
	return nil
}

// waitForPostgres blocks until dsn accepts a connection, or until budget is
// exhausted. A successful pgx.Connect is proof Postgres SPEAKS THE WIRE
// PROTOCOL — strictly stronger than "the deployment reported SUCCESS", which is
// all the CI seam can observe from outside (scripts/ci/railway-env.sh's
// ensure_postgres_running) and which was MEASURED on 2026-07-19 to be reachable
// while Postgres was not yet answering.
//
// This is the IN-NETWORK replacement for the pg_isready probe M4-22-08 deleted
// along with the public database door. It proves the same property over the
// private network, using the exact connection the caller is about to depend on,
// so no public proxy or public DSN is required to establish it.
//
// Every retry is logged at WARN naming the attempt and the remaining budget: a
// silent wait is worse than no wait, because it turns a fast, legible failure
// into a slow, illegible one.
func waitForPostgres(ctx context.Context, dsn string, budget time.Duration, logger *slog.Logger) error {
	if budget <= 0 {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}

	deadline := time.Now().Add(budget)
	for attempt := 1; ; attempt++ {
		conn, err := pgx.Connect(ctx, dsn)
		if err == nil {
			_ = conn.Close(ctx)
			if attempt > 1 {
				logger.Info("postgres is accepting connections — continuing boot-time provisioning",
					slog.Int("attempt", attempt))
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("db: postgres did not accept a connection within %s (%d attempts): %w", budget, attempt, err)
		}
		logger.Warn("postgres is not accepting connections yet — waiting before retrying",
			slog.Int("attempt", attempt),
			slog.String("budget", budget.String()),
			slog.String("error", err.Error()))

		select {
		case <-ctx.Done():
			return fmt.Errorf("db: waiting for postgres: %w", ctx.Err())
		case <-time.After(postgresWaitInterval):
		}
	}
}

// Provision runs the gateway's boot-time provisioning sequence: bootstrap
// (gated by BootstrapEnabled) → migrate (unconditional — the existing
// gateway-as-migrator behavior, unchanged) → seed (gated), short-circuiting
// fatally on the first error so a partially provisioned database is never
// served. Bootstrap and Seed each open and close their own dedicated superuser
// connection (bootstrap.go); Provision retains no DSN or connection past the
// call that used it.
//
// The guard is evaluated exactly once, from cfg.Environment/cfg.BootstrapFlag —
// never re-read from the process environment — so bootstrap and seed are
// gated identically even though migrate runs unconditionally between them.
// When the guard is false, cfg.Passwords/cfg.SuperuserDSN are never even
// looked at: Bootstrap/Seed are not called at all, so an empty/zero
// RolePasswords or an unreachable superuser DSN cannot fail a guard-off boot
// (AC-3).
func Provision(ctx context.Context, cfg ProvisionConfig) error {
	enabled := BootstrapEnabled(cfg.Environment, cfg.BootstrapFlag)

	// Named-variable validation BEFORE any dial. Without it an empty or
	// unrendered DSN reaches pgx, which reports it as a libpq-default connection
	// failure naming the process's OS user and an empty database — an error that
	// points at nothing an operator can act on. The superuser DSN is checked only
	// when the guard is on, because a guard-off (production) boot legitimately
	// runs with it unset (AC-3).
	if err := validateDSN("DATABASE_MIGRATION_URL", cfg.MigrationDSN); err != nil {
		return err
	}
	if enabled {
		if err := validateDSN("DATABASE_SUPERUSER_URL", cfg.SuperuserDSN); err != nil {
			return err
		}
	}

	// Wait on whichever DSN the sequence dials FIRST. With the guard on that is
	// the superuser DSN (Bootstrap runs before MigrateUp, and invoice_migrator
	// does not exist until Bootstrap has created it); with the guard off, the
	// migration DSN.
	firstDSN := cfg.MigrationDSN
	if enabled {
		firstDSN = cfg.SuperuserDSN
	}
	if err := waitForPostgres(ctx, firstDSN, cfg.ConnectWait, cfg.Logger); err != nil {
		return fmt.Errorf("db: provision: %w", err)
	}

	if enabled {
		if err := Bootstrap(ctx, cfg.SuperuserDSN, cfg.Passwords, cfg.BootstrapFS); err != nil {
			return fmt.Errorf("db: provision: bootstrap: %w", err)
		}
	}

	// Unconditional and unchanged: the gateway is the fleet's single in-network
	// migrator regardless of whether deploy-time provisioning is enabled for
	// this environment (docs/migrations.md §2).
	if err := MigrateUp(ctx, cfg.MigrationDSN, cfg.MigrationsFS); err != nil {
		return fmt.Errorf("db: provision: migrate: %w", err)
	}

	if enabled {
		if err := Seed(ctx, cfg.SuperuserDSN, cfg.SeedFS); err != nil {
			return fmt.Errorf("db: provision: seed: %w", err)
		}
	}

	return nil
}
