package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/tenancy"
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
	w.setPasswordFromMail(t, password)
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

// registerInvitee posts the invite registration and returns the status and body.
func (w inviteWorld) registerInvitee(t *testing.T, password string) (int, string) {
	t.Helper()
	return postGW(t, w.gw+"/auth/invitation/register", map[string]string{"token": w.token, "password": password})
}

func requireAccountExists(t *testing.T, status int, body string) {
	t.Helper()
	if status != http.StatusConflict || strings.TrimSpace(body) != `{"error":"account_exists"}` {
		t.Fatalf("invitee registration: status %d, body %q; want 409 account_exists", status, body)
	}
}

func TestIdP_ConfirmedInviteeRegisteringAgainIsToldToSignIn(t *testing.T) {
	w := newInviteWorld(t, "again-")
	w.register(t)
	w.setPasswordFromMail(t, "pw-"+uuid.NewString())

	status, body := w.previewStatus(t)
	var pv struct{ Account string }
	if err := json.Unmarshal([]byte(body), &pv); status != http.StatusOK || err != nil || pv.Account != "confirmed" {
		t.Fatalf("preview: status %d, body %s; want 200 with account confirmed", status, body)
	}
	status, body = w.registerInvitee(t, "pw-"+uuid.NewString())
	requireAccountExists(t, status, body)

	// The public route still answers a confirmed address with the 202 of a new one.
	known := idpUser{email: "public-" + uuid.NewString() + "@corp.example", password: "pw-" + uuid.NewString()}
	t.Cleanup(func() {
		_, _ = superConn(t).Exec(context.Background(), `DELETE FROM auth.users WHERE email = $1`, known.email)
	})
	known.register(t, w.base)
	confirmByLink(t, known)
	status, body = postGW(t, w.gw+"/auth/register", map[string]string{"email": known.email, "password": "pw-" + uuid.NewString()})
	if status != http.StatusAccepted {
		t.Fatalf("/auth/register for a confirmed address: status %d, body %s; want 202", status, body)
	}
}

func TestIdP_UnknownStateStillMeetsGoTruesEmptyIdentities(t *testing.T) {
	w := newPreRegisteredInviteWorld(t, "unknown-", "pw-"+uuid.NewString(), true)

	exec(t, superConn(t), `REVOKE USAGE ON SCHEMA auth FROM auth_hook_reader`)
	t.Cleanup(func() {
		if _, err := db.GrantAccountStateRead(context.Background(), mailEnv(t, "DATABASE_AUTH_ADMIN_URL")); err != nil {
			t.Errorf("re-run the grant: %v", err)
		}
	})
	status, body := w.previewStatus(t)
	var pv struct{ Account string }
	if err := json.Unmarshal([]byte(body), &pv); status != http.StatusOK || err != nil || pv.Account != "unknown" {
		t.Fatalf("preview: status %d, body %s; want 200 with account unknown", status, body)
	}

	status, body = w.registerInvitee(t, "pw-"+uuid.NewString())
	requireAccountExists(t, status, body)
}

// inviteAnother has the admin invite one more fresh address and returns it.
func (w inviteWorld) inviteAnother(t *testing.T, prefix string) string {
	t.Helper()
	email := prefix + uuid.NewString() + "@gmail.com"
	t.Cleanup(func() {
		_, _ = superConn(t).Exec(context.Background(), `DELETE FROM auth.users WHERE email = $1`, email)
	})
	inviter := &tenancy.Inviter{Store: w.store, Sender: accountmail.NewResend(w.resend.URL, "k_test", nil), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if res, err := inviter.Invite(w.adminCtx, []string{email}, "preparer"); err != nil || len(res) != 1 {
		t.Fatalf("invite %s: %+v, err %v", email, res, err)
	}
	return email
}

// listRowsFor returns the admin's list rows for one address.
func (w inviteWorld) listRowsFor(t *testing.T, email string) (rows []tenancy.Invitation, total int) {
	t.Helper()
	all, err := w.store.ListInvitations(w.adminCtx)
	if err != nil {
		t.Fatalf("ListInvitations: %v", err)
	}
	for _, r := range all {
		if r.Email == email {
			rows = append(rows, r)
		}
	}
	return rows, len(all)
}

func (w inviteWorld) requireListState(t *testing.T, want string) tenancy.Invitation {
	t.Helper()
	rows, _ := w.listRowsFor(t, w.email)
	if len(rows) != 1 || rows[0].Account != want {
		t.Fatalf("list rows for %s = %+v, want exactly one with account %q", w.email, rows, want)
	}
	return rows[0]
}

func TestIdP_InvitationsListCarriesTheAccountState(t *testing.T) {
	w := newInviteWorld(t, "liststate-")
	password := "pw-" + uuid.NewString()
	u := idpUser{email: w.email, password: password}

	// A second pending address keeps its own state while the first moves.
	other := w.inviteAnother(t, "listother-")
	first := w.requireListState(t, "none")
	check := func(want string) {
		t.Helper()
		got := w.requireListState(t, want)
		if got.ID != first.ID || got.Role != first.Role || got.Status != first.Status || got.Delivery != first.Delivery {
			t.Fatalf("row changed with the account state: %+v, was %+v", got, first)
		}
		if rows, total := w.listRowsFor(t, other); len(rows) != 1 || rows[0].Account != "none" || total != 2 {
			t.Fatalf("second invitee rows = %+v of %d; want exactly one, account none, in a list of 2", rows, total)
		}
	}

	w.register(t)
	check("unconfirmed")
	w.setPasswordFromMail(t, password)
	check("confirmed")

	// A repeat registration leaves one confirmed row.
	status, body := w.registerInvitee(t, "pw-"+uuid.NewString())
	requireAccountExists(t, status, body)
	check("confirmed")

	// Accepting removes the row from the list; the list is not empty before it.
	if _, total := w.listRowsFor(t, w.email); total == 0 {
		t.Fatal("the list is empty before the accept")
	}
	status, session := signIn(t, w.base, u)
	token, _ := session["access_token"].(string)
	if status != http.StatusOK || token == "" {
		t.Fatalf("sign in: status %d, body %v; want 200", status, session)
	}
	caller, err := idpVerifier(t, w.base).Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if code, body := w.accept(caller); code != http.StatusOK {
		t.Fatalf("accept: status %d, body %s; want 200", code, body)
	}
	if rows, total := w.listRowsFor(t, w.email); len(rows) != 0 || total != 1 {
		t.Errorf("list after the accept holds %d rows, %+v for %s; want only the second invitee's row", total, rows, w.email)
	}
}

func TestIdP_InvitationsListStateIsAdminOnly(t *testing.T) {
	w := newInviteWorld(t, "adminonly-")
	w.register(t)
	w.setPasswordFromMail(t, "pw-"+uuid.NewString())

	su := superConn(t)
	member := func(role, status string) string {
		id := uuid.NewString()
		exec(t, su, `INSERT INTO memberships (tenant_id, user_id, role, status, display_name, email) VALUES ($1, $2, $3, $4, $5, $6)`,
			w.tenant, id, role, status, role+" "+status, id+"@members.test")
		return id
	}
	// A suspended or absent member is refused earlier, as db.ErrNotActiveMember (TestInvitations_NonAdminIsRefused).
	callers := []struct {
		name, id string
		want     error
	}{
		{"active preparer", member("preparer", "active"), tenancy.ErrInviteNotPermitted},
		{"active reviewer", member("reviewer", "active"), tenancy.ErrInviteNotPermitted},
		{"suspended admin", member("admin", "suspended"), db.ErrNotActiveMember},
		{"no membership", uuid.NewString(), db.ErrNotActiveMember},
	}
	for _, c := range callers {
		ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: c.id, Role: "authenticated", TenantID: w.tenant})
		got, err := w.store.ListInvitations(ctx)
		if !errors.Is(err, c.want) || got != nil {
			t.Errorf("%s: ListInvitations = %+v, err %v; want %v and a nil slice", c.name, got, err, c.want)
		}
	}
	w.requireListState(t, "confirmed")
}

func TestIdP_InvitationsListDegradesToUnknownWithoutTheGrant(t *testing.T) {
	w := newInviteWorld(t, "listnogrant-")
	su := superConn(t)
	w.inviteAnother(t, "listnogrant2-")
	before, err := w.store.ListInvitations(w.adminCtx)
	if err != nil || len(before) != 2 {
		t.Fatalf("list before the revoke: %d rows, err %v; want 2", len(before), err)
	}
	for _, r := range before {
		if r.Account != "none" {
			t.Fatalf("control: row %s account = %q before the revoke, want none", r.Email, r.Account)
		}
	}

	exec(t, su, `REVOKE USAGE ON SCHEMA auth FROM auth_hook_reader`)
	t.Cleanup(func() {
		if _, err := db.GrantAccountStateRead(context.Background(), mailEnv(t, "DATABASE_AUTH_ADMIN_URL")); err != nil {
			t.Errorf("re-run the grant: %v", err)
		}
	})
	after, err := w.store.ListInvitations(w.adminCtx)
	if err != nil {
		t.Fatalf("ListInvitations without the grant: %v", err)
	}
	if len(after) != 2 {
		t.Fatalf("list without the grant = %d rows, want 2", len(after))
	}
	for i, r := range after {
		b := before[i]
		if r.Account != "unknown" {
			t.Errorf("row %s account = %q, want unknown", r.Email, r.Account)
		}
		if r.ID != b.ID || r.Email != b.Email || r.Role != b.Role || r.Status != b.Status || r.Delivery != b.Delivery || r.InvitedBy != b.InvitedBy {
			t.Errorf("row %d changed without the grant: %+v, was %+v", i, r, b)
		}
	}

	// Through the handler: 200, every row unknown, one WARN with the SQLSTATE and no address.
	var logs strings.Builder
	h := tenancy.InvitationsListHandler(w.store.ListInvitations, slog.New(slog.NewTextHandler(&logs, nil)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/invitations", nil).WithContext(w.adminCtx))
	var body struct {
		Invitations []struct{ Email, Account string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK || len(body.Invitations) != 2 {
		t.Fatalf("handler: status %d, body %s, err %v; want 200 with 2 items", rec.Code, rec.Body, err)
	}
	for _, it := range body.Invitations {
		if it.Account != "unknown" {
			t.Errorf("handler item account = %q, want unknown", it.Account)
		}
		if strings.Contains(logs.String(), it.Email) {
			t.Errorf("log leaks %s:\n%s", it.Email, logs.String())
		}
	}
	if n := strings.Count(logs.String(), "level=WARN"); n != 1 || !strings.Contains(logs.String(), "sqlstate=42501") {
		t.Errorf("want one WARN with sqlstate=42501, got:\n%s", logs.String())
	}
}
