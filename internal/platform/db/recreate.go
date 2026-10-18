package db

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5"
)

var migrationFileRE = regexp.MustCompile(`^(\d+)_.*\.sql$`)

// RecreateStuckSchema drops and recreates schema public when goose_db_version
// disagrees with the files in fsys: a file below the highest applied version
// that was never applied, or an applied version with no file. It returns true
// when it recreated. The auth schema is untouched. Superuser DSN: public is
// owned by pg_database_owner, which invoice_migrator cannot drop.
func RecreateStuckSchema(ctx context.Context, superuserDSN string, fsys fs.FS, logger *slog.Logger) (recreated bool, err error) {
	if logger == nil {
		logger = slog.Default()
	}
	files, err := fileVersions(fsys)
	if err != nil {
		return false, err
	}
	if len(files) == 0 {
		return false, fmt.Errorf("db: no migration files found")
	}

	conn, err := connectSuperuser(ctx, superuserDSN)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close(context.Background()) }()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, BootstrapAdvisoryLockKey); err != nil {
		return false, fmt.Errorf("db: acquire recreate advisory lock: %w", err)
	}
	defer func() {
		if _, unlockErr := conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, BootstrapAdvisoryLockKey); unlockErr != nil && err == nil {
			err = fmt.Errorf("db: release recreate advisory lock: %w", unlockErr)
		}
	}()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("db: recreate: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var ledger *string
	if err := tx.QueryRow(ctx, `SELECT to_regclass('public.goose_db_version')::text`).Scan(&ledger); err != nil {
		return false, fmt.Errorf("db: recreate: look up goose_db_version: %w", err)
	}
	if ledger == nil {
		return false, nil
	}

	rows, err := tx.Query(ctx, `SELECT version_id FROM public.goose_db_version WHERE version_id > 0`)
	if err != nil {
		return false, fmt.Errorf("db: recreate: read goose_db_version: %w", err)
	}
	applied, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return false, fmt.Errorf("db: recreate: read goose_db_version: %w", err)
	}

	missing, orphans, stuck := ledgerDrift(files, applied)
	if !stuck {
		return false, nil
	}

	logger.Warn("preview schema does not match the migration files, recreating schema public",
		"missing", missing, "orphans", orphans)

	for _, stmt := range []string{
		`SET LOCAL lock_timeout = '15s'`,
		`DROP SCHEMA public CASCADE`,
		`CREATE SCHEMA public AUTHORIZATION pg_database_owner`,
		`COMMENT ON SCHEMA public IS 'standard public schema'`,
		`GRANT USAGE ON SCHEMA public TO PUBLIC`,
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return false, fmt.Errorf("db: recreate: %s: %w", stmt, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("db: recreate: commit: %w", err)
	}
	return true, nil
}

// ledgerDrift applies goose's out-of-order rule: missing holds files with no
// ledger row below the highest ledger version (orphans count toward it),
// orphans holds ledger versions with no file; stuck is true when either is
// non-empty. Both are ascending; versions of 0 or less are ignored.
func ledgerDrift(files, applied []int64) (missing, orphans []int64, stuck bool) {
	var maxApplied int64
	appliedSet := map[int64]bool{}
	for _, v := range applied {
		if v <= 0 {
			continue
		}
		appliedSet[v] = true
		maxApplied = max(maxApplied, v)
	}
	fileSet := map[int64]bool{}
	for _, v := range files {
		fileSet[v] = true
		if !appliedSet[v] && v < maxApplied {
			missing = append(missing, v)
		}
	}
	for v := range appliedSet {
		if !fileSet[v] {
			orphans = append(orphans, v)
		}
	}
	slices.Sort(missing)
	slices.Sort(orphans)
	return missing, orphans, len(missing) > 0 || len(orphans) > 0
}

func fileVersions(fsys fs.FS) ([]int64, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("db: read migration files: %w", err)
	}
	var out []int64
	for _, e := range entries {
		m := migrationFileRE.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		v, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("db: migration version in %q: %w", e.Name(), err)
		}
		out = append(out, v)
	}
	return out, nil
}
