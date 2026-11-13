package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const purgeCall = `SELECT auth.purge_unconfirmed_account($1::uuid)`

// authAdminConn connects as supabase_auth_admin, the owner of auth.users.
func authAdminConn(t *testing.T) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), mailEnv(t, "DATABASE_AUTH_ADMIN_URL"))
	if err != nil {
		t.Fatalf("connect as supabase_auth_admin: %v", err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

// userID reads the account's id; signUpMailed returns an idpUser without one.
func userID(t *testing.T, email string) string {
	t.Helper()
	var id string
	if err := superConn(t).QueryRow(context.Background(), `SELECT id::text FROM auth.users WHERE email = $1`, email).Scan(&id); err != nil {
		t.Fatalf("read the account id: %v", err)
	}
	return id
}

// moreMailedUser registers another account on the gateway that signUpMailed already started in this test.
func moreMailedUser(t *testing.T, base, prefix string) idpUser {
	t.Helper()
	conn := superConn(t)
	u := idpUser{email: prefix + uuid.NewString() + "@example.test", password: "pw-" + uuid.NewString()}
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), `DELETE FROM auth.users WHERE email = $1`, u.email) })
	u.register(t, base)
	return u
}

// backdate sets the three mail times that the purge reads to d ago; a zero stamp is NULL.
func backdate(t *testing.T, id string, created, confirmSent, recoverySent time.Duration) {
	t.Helper()
	ago := func(d time.Duration) any {
		if d == 0 {
			return nil
		}
		return d.Seconds()
	}
	exec(t, authAdminConn(t), `UPDATE auth.users SET created_at = now() - make_interval(secs => $2),
		confirmation_sent_at = now() - make_interval(secs => $3::float8),
		recovery_sent_at = now() - make_interval(secs => $4::float8) WHERE id = $1::uuid`,
		id, created.Seconds(), ago(confirmSent), ago(recoverySent))
}

// purgeAs calls the function as invoice_app.
func purgeAs(conn *pgx.Conn, id string) (bool, error) {
	var deleted bool
	err := conn.QueryRow(context.Background(), purgeCall, id).Scan(&deleted)
	return deleted, err
}

// requirePurge requires the function's answer and, for an account that exists, its row to match it.
func requirePurge(t *testing.T, conn *pgx.Conn, label, id string, want, exists bool) {
	t.Helper()
	got, err := purgeAs(conn, id)
	if err != nil || got != want {
		t.Fatalf("%s: auth.purge_unconfirmed_account = (%v, %v); want (%v, nil)", label, got, err, want)
	}
	wantRows := 1
	if want || !exists {
		wantRows = 0
	}
	if n := countAuthRows(t, superConn(t), usersSQL, id); n != wantRows {
		t.Fatalf("%s: auth.users holds %d rows for the account after the purge, want %d", label, n, wantRows)
	}
}

const (
	day  = 24 * time.Hour
	past = 7*day + time.Minute
)

func TestIdP_PurgeFunctionIsNarrow(t *testing.T) {
	base := idpMailURL(t)
	grantAccountStateRead(t)
	su := superConn(t)

	const fn = `'auth.purge_unconfirmed_account(uuid)'::regprocedure`
	var secdef bool
	var owner string
	var config []string
	if err := su.QueryRow(context.Background(), `SELECT prosecdef, pg_get_userbyid(proowner), proconfig FROM pg_proc WHERE oid = `+fn).Scan(&secdef, &owner, &config); err != nil {
		t.Fatalf("read pg_proc for auth.purge_unconfirmed_account: %v", err)
	}
	if !secdef || owner != "supabase_auth_admin" || len(config) != 1 || config[0] != `search_path=""` {
		t.Errorf("function = secdef %v, owner %q, config %v; want true, supabase_auth_admin, [search_path=\"\"]", secdef, owner, config)
	}
	for role, want := range map[string]bool{"invoice_app": true, "invoice_gateway": false, "invoice_tenant_reader": false, "auth_hook_reader": false, "invoice_migrator": false} {
		var got bool
		if err := su.QueryRow(context.Background(), `SELECT has_function_privilege($1, `+fn+`, 'EXECUTE')`, role).Scan(&got); err != nil {
			t.Fatalf("has_function_privilege(%s): %v", role, err)
		}
		if got != want {
			t.Errorf("%s may execute = %v, want %v", role, got, want)
		}
	}
	var publicGrants int
	var hasACL bool
	if err := su.QueryRow(context.Background(), `SELECT proacl IS NOT NULL, (SELECT count(*) FROM aclexplode(proacl) a WHERE a.grantee = 0) FROM pg_proc WHERE oid = `+fn).Scan(&hasACL, &publicGrants); err != nil {
		t.Fatal(err)
	}
	if !hasACL || publicGrants != 0 {
		t.Errorf("function ACL set = %v, PUBLIC grants = %d; want an explicit ACL with no PUBLIC grant", hasACL, publicGrants)
	}

	app := appConn(t)
	fresh := signUpMailed(t, base, "narrow-fresh-", "example.test")
	freshID := userID(t, fresh.email)
	requirePurge(t, app, "fresh unconfirmed account", freshID, false, true)

	confirmed := moreMailedUser(t, base, "narrow-confirmed-")
	confirmByLink(t, confirmed)
	confirmedID := userID(t, confirmed.email)
	backdate(t, confirmedID, 8*day, 8*day, 0)
	requirePurge(t, app, "confirmed account, 8 days old", confirmedID, false, true)

	requirePurge(t, app, "unknown id", uuid.NewString(), false, false)
}

// The function alone decides: the age is the latest of created_at, confirmation_sent_at and recovery_sent_at.
func TestIdP_PurgeFunctionDeletesOnlyPastTheLatestMail(t *testing.T) {
	base := idpMailURL(t)
	grantAccountStateRead(t)
	app := appConn(t)

	for _, c := range []struct {
		name                          string
		created, confirmSent, recSent time.Duration
		extra                         string
		want                          bool
	}{
		{"created 7d+1m, no stamps", past, 0, 0, "", true},
		{"created 7d+1m, confirmation mail 7d+1m", past, past, 0, "", true},
		{"created 6d23h59m, no stamps", 7*day - time.Minute, 0, 0, "", false},
		{"created 8d, confirmation mail 1h ago", 8 * day, time.Hour, 0, "", false},
		{"created 8d, reset mail 1h ago", 8 * day, 0, time.Hour, "", false},
		{"created 8d, confirmation 8d, reset 6d", 8 * day, 8 * day, 6 * day, "", false},
		{"created 8d, confirmation 6d, reset 8d", 8 * day, 6 * day, 8 * day, "", false},
		{"past 7d but an SSO account", past, 0, 0, "is_sso_user = true", false},
		{"past 7d but an anonymous account", past, 0, 0, "is_anonymous = true", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := signUpMailed(t, base, "age-", "example.test")
			id := userID(t, u.email)
			backdate(t, id, c.created, c.confirmSent, c.recSent)
			if c.extra != "" {
				exec(t, authAdminConn(t), `UPDATE auth.users SET `+c.extra+` WHERE id = $1::uuid`, id)
			}
			requirePurge(t, app, c.name, id, c.want, true)
		})
	}
}

func TestIdP_UnconfirmedPurge_AConfirmHoldingTheRowLockWins(t *testing.T) {
	base := idpMailURL(t)
	grantAccountStateRead(t)
	app := appConn(t)
	u := signUpMailed(t, base, "lock-", "example.test")
	id := userID(t, u.email)
	backdate(t, id, 8*day, 8*day, 0)

	ctx := context.Background()
	tx, err := authAdminConn(t).Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	if _, err := tx.Exec(ctx, `UPDATE auth.users SET email_confirmed_at = now() WHERE id = $1::uuid`, id); err != nil {
		t.Fatalf("hold the row lock: %v", err)
	}

	type result struct {
		deleted bool
		err     error
	}
	done := make(chan result, 1)
	go func() {
		deleted, err := purgeAs(app, id)
		done <- result{deleted, err}
	}()

	su := superConn(t)
	waiting := false
	for deadline := time.Now().Add(5 * time.Second); !waiting && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		select {
		case r := <-done:
			t.Fatalf("the purge returned (%v, %v) while a confirm held the row lock; it must wait", r.deleted, r.err)
		default:
		}
		if err := su.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE NOT granted AND locktype IN ('transactionid', 'tuple'))`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
	}
	if !waiting {
		t.Fatal("no ungranted row lock within 5s: the purge did not wait for the confirm")
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.err != nil || r.deleted {
			t.Fatalf("purge after the confirm committed = (%v, %v); want (false, nil)", r.deleted, r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the purge did not return after the confirm committed")
	}
	var confirmed bool
	if err := su.QueryRow(ctx, `SELECT email_confirmed_at IS NOT NULL FROM auth.users WHERE id = $1::uuid`, id).Scan(&confirmed); err != nil || !confirmed {
		t.Fatalf("the account after the race: confirmed %v, err %v; want a confirmed account that still exists", confirmed, err)
	}
}
