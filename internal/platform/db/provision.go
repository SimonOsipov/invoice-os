// provision.go — the pre-deploy sequence cmd/migrate runs: recreate stuck schema (previews
// only) → bootstrap → migrate → reset (previews only) → purge → seed. Every step but the purge
// is fatal. Environment must be the raw ENVIRONMENT value:
// TestMigrateMainPassesRawEnvironmentToProvisioningGuard.
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

// ProvisionConfig is Provision's input. Environment is the raw ENVIRONMENT; see BootstrapEnabled.
type ProvisionConfig struct {
	Environment   string // raw os.Getenv("ENVIRONMENT")
	BootstrapFlag string // raw os.Getenv("GATEWAY_DB_BOOTSTRAP")

	// DeployEnv is platform.DeployEnv(), deliberately NOT ENVIRONMENT, which is
	// forked verbatim. See ResetEnabled.
	DeployEnv string
	// ResetFlag is the raw os.Getenv("GATEWAY_DB_RESET") — a SEPARATE opt-in
	// from BootstrapFlag/GATEWAY_DB_BOOTSTRAP for the strictly more dangerous
	// Reset step (reset.go). See ResetEnabled's doc comment.
	ResetFlag string

	SuperuserDSN string // DATABASE_SUPERUSER_URL; read once per gated step, never retained (QA F3)
	MigrationDSN string // DATABASE_MIGRATION_URL; always required

	Passwords    RolePasswords // ascomply.*_password GUCs Bootstrap sets
	BootstrapFS  fs.FS         // db/bootstrap.sql (db.FS)
	MigrationsFS fs.FS         // embedded migrations (migrations.FS)
	SeedFS       fs.FS         // db/seed.dev.sql (db.FS)

	// ConnectWait bounds the wait for Postgres's first connection. Zero disables it.
	ConnectWait time.Duration

	// Logger receives the wait's progress lines; nil means slog.Default().
	Logger *slog.Logger
}

// ResetWillRun reports whether Provision runs the destructive preview Reset. Provision
// branches on this method, so a caller's answer never drifts from the decision.
func (cfg ProvisionConfig) ResetWillRun() bool {
	return BootstrapEnabled(cfg.Environment, cfg.BootstrapFlag) &&
		ResetEnabled(cfg.DeployEnv, cfg.ResetFlag)
}

// ShardsWillSeed reports whether Provision seeds the topology E2E shards: in a pr-<N> preview
// that allows bootstrap. A GCP preview recreates its database instead of resetting it.
func (cfg ProvisionConfig) ShardsWillSeed() bool {
	return BootstrapEnabled(cfg.Environment, cfg.BootstrapFlag) && resettableEnvironment(cfg.DeployEnv)
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
		return fmt.Errorf("db: %s is empty — the job cannot reach Postgres without it", envVar)
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

// lockProvisionTail holds BootstrapAdvisoryLockKey on a dedicated superuser
// connection and returns its release. It serializes the destructive tail of the
// sequence — reset, purge, seed — against a peer replica running the same tail:
// purgeWithin deletes leaf-first while seed.dev.sql inserts parent-first, so
// two concurrent boots deadlock without it
// (TestProvisionConcurrentBootsSerializeCleanly). Bootstrap's key is reused
// rather than a second one, so the tail cannot race a peer's Bootstrap either;
// it is taken only after Bootstrap has released it.
func lockProvisionTail(ctx context.Context, superuserDSN string) (func(), error) {
	conn, err := connectSuperuser(ctx, superuserDSN)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, BootstrapAdvisoryLockKey); err != nil {
		_ = conn.Close(ctx)
		return nil, fmt.Errorf("db: acquire provision advisory lock: %w", err)
	}
	return func() {
		// Closing the session releases the lock server-side; the explicit
		// unlock keeps pg_locks clean for the tests that read it.
		bg := context.Background()
		_, _ = conn.Exec(bg, `SELECT pg_advisory_unlock($1)`, BootstrapAdvisoryLockKey)
		_ = conn.Close(bg)
	}, nil
}

// Provision runs the pre-deploy sequence. Bootstrap, reset, purge and seed run only when
// BootstrapEnabled allows the environment; Reset also needs ResetWillRun. Migrate always runs.
// The purge is the one non-fatal step: it is one transaction that rolls back whole.
// Provision retains no DSN or connection after it returns.
func Provision(ctx context.Context, cfg ProvisionConfig) error {
	// Set before the early returns below, so "false" is truthful on every path
	// that never reaches the purge.
	DemoPurgeOutcome = DemoPurgeSkipped

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
		// Previews only: rebuild public when the ledger and the files disagree.
		if cfg.ResetWillRun() {
			if _, err := RecreateStuckSchema(ctx, cfg.SuperuserDSN, cfg.MigrationsFS, cfg.Logger); err != nil {
				return fmt.Errorf("db: provision: recreate stuck schema: %w", err)
			}
		}

		if err := Bootstrap(ctx, cfg.SuperuserDSN, cfg.Passwords, cfg.BootstrapFS); err != nil {
			return fmt.Errorf("db: provision: bootstrap: %w", err)
		}
	}

	if err := MigrateUp(ctx, cfg.MigrationDSN, cfg.MigrationsFS); err != nil {
		return fmt.Errorf("db: provision: migrate: %w", err)
	}

	if enabled {
		// Held across reset, purge and seed — see lockProvisionTail.
		unlock, err := lockProvisionTail(ctx, cfg.SuperuserDSN)
		if err != nil {
			return fmt.Errorf("db: provision: %w", err)
		}
		defer unlock()

		// PR-environment-only, gated independently of the bootstrap/seed guard
		// above (see this function's doc comment and ResetEnabled/reset.go) —
		// runs AFTER the schema is migrated and BEFORE the purge and Seed
		// repopulate it, so a PR fork's inherited residue is gone before the
		// curated fixtures land.
		if cfg.ResetWillRun() {
			if err := Reset(ctx, cfg.SuperuserDSN); err != nil {
				return fmt.Errorf("db: provision: reset: %w", err)
			}
		}

		// The only non-fatal step in this sequence: the purge is one transaction
		// that rolls back whole, so a failure leaves the database exactly as it
		// was, and aborting here would crash-loop the fleet instead of letting
		// the deploy gate go red. TestProvisionPurgeIsTheOnlyNonFatalStep and
		// TestProvisionPurgeOrderIsResetThenPurgeThenSeed pin both properties.
		res, err := PurgeDemoTenants(ctx, cfg.SuperuserDSN)
		if err != nil {
			DemoPurgeOutcome = DemoPurgeErrored
		} else {
			DemoPurgeOutcome = DemoPurgeRan
		}
		// After the branch, not between it and the call above:
		// TestProvisionPurgeIsTheOnlyNonFatalStep pairs an err-binding
		// assignment only with the statement immediately following it.
		logPurgeResult(cfg.Logger, res, err)

		if err := Seed(ctx, cfg.SuperuserDSN, cfg.SeedFS); err != nil {
			return fmt.Errorf("db: provision: seed: %w", err)
		}

		if cfg.ShardsWillSeed() {
			if err := SeedShards(ctx, cfg.SuperuserDSN, cfg.SeedFS); err != nil {
				return fmt.Errorf("db: provision: seed shards: %w", err)
			}
		}
	}

	return nil
}
