package db

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/jackc/pgx/v5"
)

// UnconfirmedPurgeDays is the age past its last confirmation or reset mail at which an unconfirmed account is removed.
const UnconfirmedPurgeDays = 7

// GrantAccountStateRead gives auth_hook_reader the three-column read of auth.users that
// public.invitee_account_state needs, invoice_app the guarded auth.purge_unconfirmed_account, and
// invoice_gateway auth.confirm_link_address. It runs as supabase_auth_admin and reports false, with
// no write, while GoTrue has not yet created auth.users.
// ceiling: any invoice_app service can delete one account by id that is unconfirmed past the age
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
		purgeFunctionSQL,
		`REVOKE EXECUTE ON FUNCTION auth.purge_unconfirmed_account(uuid) FROM PUBLIC`,
		revokeStrayPurgeGrantsSQL,
		`GRANT USAGE ON SCHEMA auth TO invoice_app`,
		`GRANT EXECUTE ON FUNCTION auth.purge_unconfirmed_account(uuid) TO invoice_app`,
		confirmLinkAddressSQL,
		`REVOKE EXECUTE ON FUNCTION auth.confirm_link_address(text) FROM PUBLIC`,
		revokeStrayLinkAddressGrantsSQL,
		`GRANT USAGE ON SCHEMA auth TO invoice_gateway`,
		`GRANT EXECUTE ON FUNCTION auth.confirm_link_address(text) TO invoice_gateway`,
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

// Leaves EXECUTE to the owner and invoice_app only, so a stray grant does not survive a boot.
const revokeStrayPurgeGrantsSQL = `DO $$ DECLARE r oid; BEGIN
  FOR r IN SELECT a.grantee FROM pg_proc p, aclexplode(p.proacl) a
   WHERE p.oid = 'auth.purge_unconfirmed_account(uuid)'::regprocedure AND a.grantee NOT IN (0, p.proowner, 'invoice_app'::regrole)
  LOOP
    EXECUTE format('REVOKE EXECUTE ON FUNCTION auth.purge_unconfirmed_account(uuid) FROM %I', pg_get_userbyid(r));
  END LOOP;
END $$`

// Leaves EXECUTE to the owner and invoice_gateway only, so a stray grant does not survive a boot.
const revokeStrayLinkAddressGrantsSQL = `DO $$ DECLARE r oid; BEGIN
  FOR r IN SELECT a.grantee FROM pg_proc p, aclexplode(p.proacl) a
   WHERE p.oid = 'auth.confirm_link_address(text)'::regprocedure AND a.grantee NOT IN (0, p.proowner, 'invoice_gateway'::regrole)
  LOOP
    EXECUTE format('REVOKE EXECUTE ON FUNCTION auth.confirm_link_address(text) FROM %I', pg_get_userbyid(r));
  END LOOP;
END $$`

// Mirrors GoTrue's FindOneTimeToken: expiry is not checked, so an expired unredeemed link still names its account.
const confirmLinkAddressSQL = `CREATE OR REPLACE FUNCTION auth.confirm_link_address(p_token_hash text) RETURNS text
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = '' AS $fn$
DECLARE v_email text;
BEGIN
  SELECT u.email INTO v_email
    FROM auth.one_time_tokens t JOIN auth.users u ON u.id = t.user_id
   WHERE t.token_type = 'confirmation_token' AND t.token_hash = p_token_hash
   LIMIT 1;
  IF v_email IS NULL THEN RETURN NULL; END IF;
  RETURN pg_catalog.left(v_email, 1) || '***@' || pg_catalog.substr(v_email, pg_catalog.strpos(v_email, '@') + 1);
END $fn$`

// The DELETE re-checks confirmation and age under the row lock, so a confirm that commits first wins.
var purgeFunctionSQL = fmt.Sprintf(`CREATE OR REPLACE FUNCTION auth.purge_unconfirmed_account(p_user_id uuid) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path = '' AS $fn$
DECLARE n integer;
BEGIN
  DELETE FROM auth.users u
   WHERE u.id = p_user_id
     AND u.email_confirmed_at IS NULL
     AND NOT u.is_sso_user
     AND NOT u.is_anonymous
     AND GREATEST(u.created_at, u.confirmation_sent_at, u.recovery_sent_at) < pg_catalog.now() - interval '%d days';
  GET DIAGNOSTICS n = ROW_COUNT;
  RETURN n > 0;
END $fn$`, UnconfirmedPurgeDays)

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
