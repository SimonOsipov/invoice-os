// Command migrate prepares the database before each deploy: migrations under goose's lock, the
// preview reset, purge and seeds, then the account-state grant. `migrate bootstrap` creates the
// roles and sets their passwords; run it once per database and after a role is added.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	dbsql "github.com/SimonOsipov/invoice-os/db"
	"github.com/SimonOsipov/invoice-os/internal/customer"
	"github.com/SimonOsipov/invoice-os/internal/demodocs"
	"github.com/SimonOsipov/invoice-os/internal/demopolicy"
	"github.com/SimonOsipov/invoice-os/internal/document"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

func main() {
	defer platform.ReportBootPanic()
	migrate(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
}

func migrate(log *slog.Logger) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	// Raw ENVIRONMENT, never a defaulted one: BootstrapEnabled is an allowlist.
	cfg := db.ProvisionConfig{
		Environment:   os.Getenv("ENVIRONMENT"),
		BootstrapFlag: os.Getenv("GATEWAY_DB_BOOTSTRAP"),
		DeployEnv:     platform.DeployEnv(),
		ResetFlag:     os.Getenv("GATEWAY_DB_RESET"),
		SuperuserDSN:  os.Getenv("DATABASE_SUPERUSER_URL"),
		MigrationDSN:  os.Getenv("DATABASE_MIGRATION_URL"),
		Passwords: db.RolePasswords{
			Migrator:  os.Getenv("MIGRATOR_PASSWORD"),
			App:       os.Getenv("APP_PASSWORD"),
			Reader:    os.Getenv("READER_PASSWORD"),
			AuthAdmin: os.Getenv("AUTH_ADMIN_PASSWORD"),
			Gateway:   os.Getenv("GATEWAY_PASSWORD"),
		},
		BootstrapFS:  dbsql.FS,
		MigrationsFS: migrations.FS,
		SeedFS:       dbsql.FS,
		ConnectWait:  connectWait,
		Logger:       log,
	}
	if err := run(ctx, os.Args[1:], cfg, db.GrantAccountStateRead, seedDemo, log); err != nil {
		platform.Fatal(log, "migrate: %v", err)
	}
}

// connectWait covers a preview database that is still starting when the job runs.
const connectWait = 120 * time.Second

func run(ctx context.Context, args []string, cfg db.ProvisionConfig, grant func(context.Context, string) (bool, error), seed func(context.Context, *slog.Logger), log *slog.Logger) error {
	switch {
	case len(args) == 1 && args[0] == "bootstrap":
		if cfg.SuperuserDSN == "" {
			return errors.New("bootstrap: DATABASE_SUPERUSER_URL is empty")
		}
		if err := db.Bootstrap(ctx, cfg.SuperuserDSN, cfg.Passwords, cfg.BootstrapFS); err != nil {
			return err
		}
		log.Info("migrate: roles bootstrapped")
		return nil
	case len(args) > 0:
		return fmt.Errorf("unknown arguments %q, want none or \"bootstrap\"", args)
	}

	if err := db.Provision(ctx, cfg); err != nil {
		return err
	}
	log.Info("migrate: schema ready", "db_reset", cfg.ResetWillRun(), "demo_purge", string(db.DemoPurgeOutcome))
	if db.BootstrapEnabled(cfg.Environment, cfg.BootstrapFlag) {
		seed(ctx, log)
	}
	grantAccountStateRead(ctx, cfg.MigrationDSN, cfg.Passwords.AuthAdmin, grant, log)
	return nil
}

// grantAccountStateRead runs the auth.users grant once. It warns and continues when GoTrue has
// not yet created auth.users; the next run grants. It never logs the error text: it can hold the DSN.
func grantAccountStateRead(ctx context.Context, migrationDSN, password string, grant func(context.Context, string) (bool, error), log *slog.Logger) {
	dsn, err := db.AuthAdminDSN(migrationDSN, password)
	if err != nil {
		log.Warn("migrate: account state read not granted", "reason", err.Error())
		return
	}
	ok, err := grant(ctx, dsn)
	if err == nil && ok {
		log.Info("migrate: account state read granted")
		return
	}
	log.Warn("migrate: account state read not granted", "class", grantFailureClass(err))
}

// grantFailureClass names a grant failure without its text, which can hold the DSN.
func grantFailureClass(err error) string {
	if err == nil {
		return "not_ready"
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return "unknown"
}

// seedDemo gives the SQL-seeded demo tenants their source documents, an active approval policy
// and customers, through the app role. Non-fatal: a missing demo row costs one assertion.
func seedDemo(ctx context.Context, log *slog.Logger) {
	pool, err := db.NewPool(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Error("migrate: demo seeds skipped", "error", err)
		return
	}
	defer pool.Close()
	docCfg, err := document.ConfigFromEnv()
	if err != nil {
		log.Error("migrate: demo seeds skipped", "error", err)
		return
	}
	objects, err := document.NewS3Store(docCfg, nil)
	if err != nil {
		log.Error("migrate: demo seeds skipped", "error", err)
		return
	}
	docs := document.NewStore(pool)
	if res, err := demodocs.Seed(ctx, pool, document.NewService(docs, objects).Store, log); err != nil {
		log.Error("migrate: demo source documents", "error", err,
			"documents", res.DocumentsStored, "invoices", res.InvoicesLinked)
	}
	// Bounded: the policy sweep takes FOR UPDATE row locks.
	policyCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	if res, err := demopolicy.Seed(policyCtx, pool, log); err != nil {
		log.Error("migrate: demo approval policy", "error", err,
			"outcome", res.Note, "backlog_found", res.BacklogFound, "runs_armed", res.RunsArmed)
	}
	cancel()
	custCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := customer.SeedDemo(custCtx, pool, platform.DeployEnv(), log); err != nil {
		log.Error("migrate: demo customers", "error", err)
	}
}
