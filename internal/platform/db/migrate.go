package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

// MigrateUp applies every pending migration in fsys to the database at dsn. It is
// the single on-deploy migration entry point: the gateway calls it at boot,
// before it starts serving, so the schema is fully migrated before the fleet
// answers traffic — the gateway-as-migrator mechanism in docs/migrations.md §2.
//
// dsn MUST be the migrator connection string (DATABASE_MIGRATION_URL). Never the
// app role (it holds no DDL privileges) and never the superuser (BYPASSRLS would
// let a migration silently defeat RLS). The context services never call this;
// they boot against an already-migrated schema (AC #4/#5).
//
// It opens a dedicated database/sql connection through the pgx v5 stdlib driver —
// the same driver `go tool goose` uses locally and in CI — runs goose's Provider
// Up, and closes the connection. goose applies only what is pending, so a redeploy
// with no new migrations is a clean no-op. Any error is returned so the caller
// can fail fast (a gateway that cannot migrate must not come up healthy).
func MigrateUp(ctx context.Context, dsn string, fsys fs.FS) error {
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("db: open migration connection: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()

	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, fsys)
	if err != nil {
		return fmt.Errorf("db: build migration provider: %w", err)
	}

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("db: apply migrations: %w", err)
	}
	return nil
}
