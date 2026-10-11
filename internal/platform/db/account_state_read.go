package db

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/jackc/pgx/v5"
)

// GrantAccountStateRead gives auth_hook_reader the three-column read of auth.users that
// public.invitee_account_state needs. It runs as supabase_auth_admin and reports false, with
// no write, while GoTrue has not yet created auth.users.
// ceiling: one connection per call; it runs once at boot, so pool it if a hot caller appears
func GrantAccountStateRead(ctx context.Context, authAdminDSN string) (bool, error) {
	conn, err := pgx.Connect(ctx, authAdminDSN)
	if err != nil {
		return false, fmt.Errorf("db: grant account state read: connect: %w", err)
	}
	defer conn.Close(context.Background())
	tx, err := conn.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("db: grant account state read: begin: %w", err)
	}
	defer tx.Rollback(context.Background())
	var present bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('auth.users') IS NOT NULL`).Scan(&present); err != nil {
		return false, fmt.Errorf("db: grant account state read: probe: %w", err)
	}
	if !present {
		return false, nil
	}
	for _, stmt := range []string{
		`GRANT USAGE ON SCHEMA auth TO auth_hook_reader`,
		`GRANT SELECT (email, email_confirmed_at, is_sso_user) ON auth.users TO auth_hook_reader`,
		`DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = 'auth' AND tablename = 'users' AND policyname = 'invitee_account_state_read') THEN
    CREATE POLICY invitee_account_state_read ON auth.users FOR SELECT TO auth_hook_reader USING (true);
  END IF;
END $$`,
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return false, fmt.Errorf("db: grant account state read: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("db: grant account state read: commit: %w", err)
	}
	return true, nil
}

// AuthAdminDSN swaps the credentials of migrationDSN for supabase_auth_admin and password.
// Its errors hold neither the DSN nor the password.
func AuthAdminDSN(migrationDSN, password string) (string, error) {
	if password == "" {
		return "", errors.New("db: auth admin dsn: empty password")
	}
	u, err := url.Parse(migrationDSN)
	if err != nil {
		return "", errors.New("db: auth admin dsn: unparseable migration dsn")
	}
	if u.Host == "" {
		return "", errors.New("db: auth admin dsn: migration dsn has no host")
	}
	u.User = url.UserPassword("supabase_auth_admin", password)
	return u.String(), nil
}
