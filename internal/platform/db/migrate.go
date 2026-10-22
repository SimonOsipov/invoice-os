package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

// MigrateUp applies every pending migration in fsys to the database at dsn, as the migrator
// (DATABASE_MIGRATION_URL). Never pass the app role (no DDL) or the superuser (BYPASSRLS).
// goose's session advisory lock serializes concurrent runs: TestMigrateUp_WaitsForTheMigrationLock.
func MigrateUp(ctx context.Context, dsn string, fsys fs.FS) error {
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("db: open migration connection: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()

	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("db: build migration locker: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, fsys, goose.WithSessionLocker(locker))
	if err != nil {
		return fmt.Errorf("db: build migration provider: %w", err)
	}

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("db: apply migrations: %w", err)
	}
	return nil
}
