package auth_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const stateCall = `SELECT public.invitee_account_state($1::text)`

var (
	grantOnce    sync.Once
	grantGranted bool
	grantErr     error
)

// grantAccountStateRead gives auth_hook_reader its three-column read once per run, as supabase_auth_admin.
func grantAccountStateRead(t *testing.T) {
	t.Helper()
	dsn := mailEnv(t, "DATABASE_AUTH_ADMIN_URL")
	grantOnce.Do(func() { grantGranted, grantErr = db.GrantAccountStateRead(context.Background(), dsn) })
	if grantErr != nil || !grantGranted {
		t.Fatalf("GrantAccountStateRead = (%v, %v); want (true, nil)", grantGranted, grantErr)
	}
}

// appConn connects as invoice_app, the only role that may call the function.
func appConn(t *testing.T) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), mailEnv(t, "DATABASE_URL"))
	if err != nil {
		t.Fatalf("connect as invoice_app: %v", err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

// accountStateAs calls the function as invoice_app; email nil is SQL NULL.
func accountStateAs(conn *pgx.Conn, email any) (string, error) {
	var state string
	err := conn.QueryRow(context.Background(), stateCall, email).Scan(&state)
	return state, err
}

func requireState(t *testing.T, conn *pgx.Conn, email any, want string) {
	t.Helper()
	got, err := accountStateAs(conn, email)
	if err != nil || got != want {
		t.Fatalf("invitee_account_state(%v) = %q, err %v; want %q", email, got, err, want)
	}
}

func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// newMailedUser names a fresh address and serves the verify page its mailed link targets; nothing is registered yet.
func newMailedUser(t *testing.T, base, prefix, domain string) idpUser {
	t.Helper()
	startGateway(t, base, 0, nil)
	conn := superConn(t)
	u := idpUser{email: prefix + uuid.NewString() + "@" + domain, password: "pw-" + uuid.NewString()}
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), `DELETE FROM auth.users WHERE email = $1`, u.email) })
	return u
}

// signUpMailed registers through idp-mail's own /signup, without clicking the mailed link.
func signUpMailed(t *testing.T, base, prefix, domain string) idpUser {
	t.Helper()
	u := newMailedUser(t, base, prefix, domain)
	u.register(t, base)
	return u
}

func (u idpUser) register(t *testing.T, base string) {
	t.Helper()
	if status, body := postJSON(t, base+"/signup", map[string]string{"email": u.email, "password": u.password}); status != http.StatusOK {
		t.Fatalf("POST /signup for %s: status %d, body %v; want 200", u.email, status, body)
	}
}

func confirmByLink(t *testing.T, u idpUser) {
	t.Helper()
	if got := follow(t, confirmationLink(t, u.email)); got != siteURL+"/?verified=1" {
		t.Fatalf("verify redirect = %q, want %s/?verified=1", got, siteURL)
	}
}

func TestIdP_InviteeAccountStateNamesTheThreeStates(t *testing.T) {
	base := idpMailURL(t)
	grantAccountStateRead(t)
	app := appConn(t)
	u := newMailedUser(t, base, "state-", "example.test")

	requireState(t, app, u.email, "none")
	u.register(t, base)
	requireState(t, app, u.email, "unconfirmed")
	confirmByLink(t, u)
	requireState(t, app, u.email, "confirmed")
}

func TestIdP_InviteeAccountStateMatchesCaseAndSpace(t *testing.T) {
	base := idpMailURL(t)
	grantAccountStateRead(t)
	app := appConn(t)
	u := signUpMailed(t, base, "x-", "corp.example")
	confirmByLink(t, u)

	requireState(t, app, u.email, "confirmed")
	requireState(t, app, "  "+strings.ToUpper(u.email)+" ", "confirmed")
	requireState(t, app, nil, "none")
	requireState(t, app, "", "none")
	requireState(t, app, "   ", "none")

	// Equality, not a pattern or a prefix: a wildcard or a cut address names no account.
	local, domain, _ := strings.Cut(u.email, "@")
	for _, probe := range []string{"%", "_", local + "@%", "%@" + domain, u.email[:len(u.email)-1], u.email + "x"} {
		requireState(t, app, probe, "none")
	}
}

func TestIdP_InviteeAccountStateIgnoresSSORows(t *testing.T) {
	base := idpMailURL(t)
	grantAccountStateRead(t)
	app := appConn(t)
	su := superConn(t)
	u := signUpMailed(t, base, "sso-", "example.test")
	confirmByLink(t, u)
	requireState(t, app, u.email, "confirmed")

	exec(t, su, `UPDATE auth.users SET is_sso_user = true WHERE email = $1`, u.email)
	t.Cleanup(func() {
		_, _ = su.Exec(context.Background(), `UPDATE auth.users SET is_sso_user = false WHERE email = $1`, u.email)
	})
	requireState(t, app, u.email, "none")
}

func TestIdP_AppCannotReadAuthUsersDirectly(t *testing.T) {
	idpMailURL(t)
	grantAccountStateRead(t)
	app := appConn(t)

	var email string
	err := app.QueryRow(context.Background(), `SELECT email FROM auth.users LIMIT 1`).Scan(&email)
	if err == nil || sqlState(err) != "42501" {
		t.Fatalf("invoice_app SELECT email FROM auth.users: err %v, SQLSTATE %q; want 42501", err, sqlState(err))
	}
	requireState(t, appConn(t), "never-"+uuid.NewString()+"@example.test", "none")

	su := superConn(t)
	for _, role := range []string{"invoice_tenant_reader", "invoice_migrator"} {
		tx, err := su.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(context.Background(), `SET LOCAL ROLE `+role); err != nil {
			t.Fatalf("SET LOCAL ROLE %s: %v", role, err)
		}
		err = tx.QueryRow(context.Background(), `SELECT email FROM auth.users LIMIT 1`).Scan(&email)
		_ = tx.Rollback(context.Background())
		if sqlState(err) != "42501" {
			t.Errorf("%s SELECT email FROM auth.users: err %v, SQLSTATE %q; want 42501", role, err, sqlState(err))
		}
	}
}

type grantShape struct {
	columns  []string
	tablePri int
	policies []string
	usage    bool
	create   bool
	acls     string
}

func readGrantShape(t *testing.T, conn *pgx.Conn) grantShape {
	t.Helper()
	ctx := context.Background()
	var s grantShape
	rows, err := conn.Query(ctx, `SELECT column_name || ':' || privilege_type FROM information_schema.column_privileges
		WHERE grantee = 'auth_hook_reader' AND table_schema = 'auth' AND table_name = 'users'`)
	if err != nil {
		t.Fatalf("read column privileges: %v", err)
	}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		s.columns = append(s.columns, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(s.columns)
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM information_schema.table_privileges
		WHERE grantee = 'auth_hook_reader' AND table_schema = 'auth'`).Scan(&s.tablePri); err != nil {
		t.Fatalf("read table privileges: %v", err)
	}
	prow, err := conn.Query(ctx, `SELECT policyname || '|' || cmd || '|' || roles::text || '|' || coalesce(qual, '') FROM pg_policies
		WHERE schemaname = 'auth' AND tablename = 'users' AND 'auth_hook_reader' = ANY (roles)`)
	if err != nil {
		t.Fatalf("read policies: %v", err)
	}
	for prow.Next() {
		var p string
		if err := prow.Scan(&p); err != nil {
			t.Fatal(err)
		}
		s.policies = append(s.policies, p)
	}
	prow.Close()
	if err := conn.QueryRow(ctx, `SELECT has_schema_privilege('auth_hook_reader', 'auth', 'USAGE'),
		has_schema_privilege('auth_hook_reader', 'auth', 'CREATE'),
		coalesce((SELECT nspacl::text FROM pg_namespace WHERE nspname = 'auth'), '') || coalesce((SELECT relacl::text FROM pg_class WHERE oid = 'auth.users'::regclass), '')
			|| coalesce((SELECT string_agg(attacl::text, ',' ORDER BY attname) FROM pg_attribute WHERE attrelid = 'auth.users'::regclass AND attacl IS NOT NULL), '')`,
	).Scan(&s.usage, &s.create, &s.acls); err != nil {
		t.Fatalf("read schema privileges: %v", err)
	}
	return s
}

func TestIdP_AccountStateReadGrantIsExactAndRepeatable(t *testing.T) {
	idpMailURL(t)
	dsn := mailEnv(t, "DATABASE_AUTH_ADMIN_URL")
	su := superConn(t)
	ctx := context.Background()

	// Start from nothing so each statement of the grant has to do its own work.
	for _, stmt := range []string{
		`REVOKE ALL ON auth.users FROM auth_hook_reader`,
		`REVOKE ALL ON SCHEMA auth FROM auth_hook_reader`,
		`DROP POLICY IF EXISTS invitee_account_state_read ON auth.users`,
	} {
		exec(t, su, stmt)
	}
	t.Cleanup(func() {
		if _, err := db.GrantAccountStateRead(context.Background(), dsn); err != nil {
			t.Errorf("re-run the grant: %v", err)
		}
	})

	if granted, err := db.GrantAccountStateRead(ctx, dsn); err != nil || !granted {
		t.Fatalf("first GrantAccountStateRead = (%v, %v); want (true, nil)", granted, err)
	}
	first := readGrantShape(t, su)
	if granted, err := db.GrantAccountStateRead(ctx, dsn); err != nil || !granted {
		t.Fatalf("second GrantAccountStateRead = (%v, %v); want (true, nil)", granted, err)
	}
	if second := readGrantShape(t, su); !reflect.DeepEqual(first, second) {
		t.Errorf("second run changed the grant:\nfirst  %+v\nsecond %+v", first, second)
	}

	wantCols := []string{"email:SELECT", "email_confirmed_at:SELECT", "is_sso_user:SELECT"}
	if !reflect.DeepEqual(first.columns, wantCols) {
		t.Errorf("auth_hook_reader column privileges on auth.users = %v, want %v", first.columns, wantCols)
	}
	if first.tablePri != 0 {
		t.Errorf("auth_hook_reader holds %d table-level privileges in schema auth, want 0", first.tablePri)
	}
	if want := []string{"invitee_account_state_read|SELECT|{auth_hook_reader}|true"}; !reflect.DeepEqual(first.policies, want) {
		t.Errorf("auth_hook_reader policies on auth.users = %v, want %v", first.policies, want)
	}
	if !first.usage || first.create {
		t.Errorf("auth_hook_reader on schema auth: USAGE %v CREATE %v; want true false", first.usage, first.create)
	}

	asHookReader := func(sql string) error {
		tx, err := su.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE auth_hook_reader`); err != nil {
			t.Fatalf("SET LOCAL ROLE auth_hook_reader: %v", err)
		}
		_, err = tx.Exec(ctx, sql)
		return err
	}
	for _, col := range []string{"email", "email_confirmed_at", "is_sso_user"} {
		if err := asHookReader(`SELECT count(` + col + `) FROM auth.users`); err != nil {
			t.Errorf("control: auth_hook_reader reading %s: %v", col, err)
		}
	}
	for _, refused := range []string{
		`SELECT count(encrypted_password) FROM auth.users`,
		`SELECT count(id) FROM auth.users`,
		`SELECT count(raw_user_meta_data) FROM auth.users`,
		`SELECT * FROM auth.users`,
		`UPDATE auth.users SET email = email`,
		`DELETE FROM auth.users`,
	} {
		if err := asHookReader(refused); sqlState(err) != "42501" {
			t.Errorf("auth_hook_reader %q: err %v, SQLSTATE %q; want 42501", refused, err, sqlState(err))
		}
	}
}

func TestIdP_AccountStateReadGrantWaitsForGoTrue(t *testing.T) {
	idpMailURL(t)
	su := superConn(t)
	ctx := context.Background()
	scratch := "grant_wait_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]

	exec(t, su, `CREATE DATABASE `+scratch+` OWNER supabase_auth_admin`)
	t.Cleanup(func() { _, _ = su.Exec(context.Background(), `DROP DATABASE IF EXISTS `+scratch+` WITH (FORCE)`) })

	inScratch := func(dsn string) string {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		u.Path = "/" + scratch
		return u.String()
	}
	granted, err := db.GrantAccountStateRead(ctx, inScratch(mailEnv(t, "DATABASE_AUTH_ADMIN_URL")))
	if err != nil || granted {
		t.Fatalf("GrantAccountStateRead without auth.users = (%v, %v); want (false, nil)", granted, err)
	}

	conn, err := pgx.Connect(ctx, inScratch(requireEnv(t, "DATABASE_SUPERUSER_URL")))
	if err != nil {
		t.Fatalf("connect to %s: %v", scratch, err)
	}
	defer conn.Close(ctx)
	var policies int
	var hasAuth bool
	if err := conn.QueryRow(ctx, `SELECT (SELECT count(*) FROM pg_policies), to_regnamespace('auth') IS NOT NULL`).Scan(&policies, &hasAuth); err != nil {
		t.Fatalf("read %s: %v", scratch, err)
	}
	if policies != 0 || hasAuth {
		t.Errorf("after a refused grant %s holds %d policies, auth schema %v; want 0, false", scratch, policies, hasAuth)
	}
}

func TestIdP_InviteeAccountStateWithoutTheGrantFailsClosed(t *testing.T) {
	idpMailURL(t)
	grantAccountStateRead(t)
	su := superConn(t)
	app := appConn(t)
	probe := "never-" + uuid.NewString() + "@example.test"
	requireState(t, app, probe, "none")

	exec(t, su, `REVOKE USAGE ON SCHEMA auth FROM auth_hook_reader`)
	t.Cleanup(func() {
		if _, err := db.GrantAccountStateRead(context.Background(), mailEnv(t, "DATABASE_AUTH_ADMIN_URL")); err != nil {
			t.Errorf("re-run the grant: %v", err)
		}
	})
	state, err := accountStateAs(app, probe)
	if sqlState(err) != "42501" || state != "" {
		t.Fatalf("call without the grant = %q, err %v (SQLSTATE %q); want an error 42501 and no row", state, err, sqlState(err))
	}
}

func TestIdP_PreviewCarriesTheAccountState(t *testing.T) {
	w := newInviteWorld(t, "state-")
	ctx := context.Background()
	check := func(want string) {
		t.Helper()
		p, err := w.store.PreviewInvitation(ctx, w.token)
		if err != nil {
			t.Fatalf("PreviewInvitation: %v", err)
		}
		if p.Workspace != "IdP Invite Co" || p.Role != "reviewer" || p.Email != w.email || p.Account != want {
			t.Fatalf("preview = %+v, want IdP Invite Co / reviewer / %s / account %q", p, w.email, want)
		}
	}
	check("none")

	password := "pw-" + uuid.NewString()
	status, body := postJSON(t, w.gw+"/auth/invitation/register", map[string]string{"token": w.token, "password": password})
	if status != http.StatusAccepted {
		t.Fatalf("register through the gateway: status %d, body %v; want 202", status, body)
	}
	check("unconfirmed")
	confirmByLink(t, idpUser{email: w.email, password: password})
	check("confirmed")
}

func TestIdP_PreviewDegradesToUnknownWithoutTheGrant(t *testing.T) {
	w := newInviteWorld(t, "nogrant-")
	exec(t, superConn(t), `REVOKE USAGE ON SCHEMA auth FROM auth_hook_reader`)
	t.Cleanup(func() {
		if _, err := db.GrantAccountStateRead(context.Background(), mailEnv(t, "DATABASE_AUTH_ADMIN_URL")); err != nil {
			t.Errorf("re-run the grant: %v", err)
		}
	})

	p, err := w.store.PreviewInvitation(context.Background(), w.token)
	if err != nil {
		t.Fatalf("PreviewInvitation without the grant: %v", err)
	}
	if p.Workspace != "IdP Invite Co" || p.Role != "reviewer" || p.Email != w.email || p.Account != "unknown" {
		t.Fatalf("preview = %+v, want the three fields intact and account unknown", p)
	}
}
