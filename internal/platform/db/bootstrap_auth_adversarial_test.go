package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Names start with TestBootstrapSQL so ci.yml's TestBootstrap filter and the frozen
// pre-fix filter in ci_filter_coverage_test.go both reach them.

const sqlstateInsufficientPrivilege = "42501"

// applyDevBootstrap puts the shared DB at the bootstrap baseline with the dev passwords.
func applyDevBootstrap(t *testing.T, pool *pgxpool.Pool, sql string) {
	t.Helper()
	if err := applyBootstrap(t, pool, devDefaultGUCs(), sql); err != nil {
		t.Fatalf("apply db/bootstrap.sql: %v", err)
	}
}

// connectAs opens one connection as role/password, closed on cleanup.
func connectAs(t *testing.T, superDSN, role, password string) *pgx.Conn {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, loginDSN(t, superDSN, role, password))
	if err != nil {
		t.Fatalf("login as %s: %v", role, err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func sqlstate(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func schemaOwner(t *testing.T, pool *pgxpool.Pool, schema string) (string, bool) {
	t.Helper()
	var owner string
	err := pool.QueryRow(context.Background(),
		`SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname = $1`, schema,
	).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false
	}
	if err != nil {
		t.Fatalf("read owner of schema %s: %v", schema, err)
	}
	return owner, true
}

type hookMembership struct{ inherit, set, admin bool }

func hookReaderMemberships(t *testing.T, pool *pgxpool.Pool) []hookMembership {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT m.inherit_option, m.set_option, m.admin_option
		FROM pg_auth_members m
		WHERE m.roleid = 'auth_hook_reader'::regrole AND m.member = 'invoice_migrator'::regrole`)
	if err != nil {
		t.Fatalf("read pg_auth_members: %v", err)
	}
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (hookMembership, error) {
		var m hookMembership
		return m, r.Scan(&m.inherit, &m.set, &m.admin)
	})
	if err != nil {
		t.Fatalf("scan pg_auth_members: %v", err)
	}
	return got
}

func TestBootstrapSQLAuthCreatesTheAuthSchemaWhenAbsent(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	sql := readBootstrapSQL(t)
	applyDevBootstrap(t, pool, sql)

	// Park the existing schema (it may hold objects) instead of dropping it.
	parked := "auth_qa_" + uuid.NewString()[:8]
	mustExecSQL(t, pool, `ALTER SCHEMA auth RENAME TO `+parked)
	t.Cleanup(func() {
		ctx := context.Background()
		if _, ok := schemaOwner(t, pool, "auth"); ok {
			if _, err := pool.Exec(ctx, `DROP SCHEMA auth`); err != nil {
				t.Errorf("drop the schema this test's bootstrap created: %v", err)
				return
			}
		}
		if _, err := pool.Exec(ctx, `ALTER SCHEMA `+parked+` RENAME TO auth`); err != nil {
			t.Errorf("restore the parked auth schema %s: %v", parked, err)
		}
	})
	if _, ok := schemaOwner(t, pool, "auth"); ok {
		t.Fatalf("schema auth still exists after parking it, so this test cannot observe creation")
	}

	applyDevBootstrap(t, pool, sql)

	owner, ok := schemaOwner(t, pool, "auth")
	if !ok {
		t.Fatalf("schema auth not created by bootstrap")
	}
	if owner != authAdminRole {
		t.Errorf("schema auth owner = %q, want %q", owner, authAdminRole)
	}
}

func TestBootstrapSQLAuthAdminCannotSetRoleToAnyOtherRole(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	t.Cleanup(func() {
		mustExecSQL(t, pool, `REVOKE auth_hook_reader, invoice_migrator, invoice_app, invoice_tenant_reader FROM supabase_auth_admin`)
	})
	applyDevBootstrap(t, pool, readBootstrapSQL(t))
	ctx := context.Background()
	def := devDefaultGUCs()

	// Control: the migrator's SET-only membership does allow SET ROLE, so a refusal below is not the harness.
	mig := connectAs(t, superDSN, "invoice_migrator", def.migrator.value)
	if _, err := mig.Exec(ctx, `SET ROLE auth_hook_reader`); err != nil {
		t.Fatalf("control: invoice_migrator SET ROLE auth_hook_reader: %v", err)
	}
	var cur string
	if err := mig.QueryRow(ctx, `SELECT current_user`).Scan(&cur); err != nil || cur != hookReaderRole {
		t.Fatalf("control: current_user after SET ROLE = %q (err %v), want %s", cur, err, hookReaderRole)
	}

	aa := connectAs(t, superDSN, authAdminRole, def.authAdmin.value)
	targets := []string{hookReaderRole, "invoice_migrator", "invoice_app", "invoice_tenant_reader"}
	for _, target := range targets {
		_, err := aa.Exec(ctx, `SET ROLE `+target)
		if err == nil {
			t.Errorf("%s: SET ROLE %s succeeded, want permission denied", authAdminRole, target)
			_, _ = aa.Exec(ctx, `RESET ROLE`)
			continue
		}
		if code := sqlstate(err); code != sqlstateInsufficientPrivilege {
			t.Errorf("%s: SET ROLE %s: SQLSTATE %q (%v), want %s", authAdminRole, target, code, err, sqlstateInsufficientPrivilege)
		}
	}
}

func TestBootstrapSQLAuthHookReaderCannotLogInEvenWithAPassword(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	sql := readBootstrapSQL(t)
	t.Cleanup(func() {
		mustExecSQL(t, pool, `ALTER ROLE auth_hook_reader WITH NOLOGIN PASSWORD NULL`)
		restoreDevDefaultPasswords(t, pool)
	})

	// Drifted to LOGIN with a known password: only bootstrap's NOLOGIN can refuse the login.
	pw := "hook-login-" + uuid.NewString()
	alterRolePassword(t, pool, hookReaderRole, pw)
	mustExecSQL(t, pool, `ALTER ROLE auth_hook_reader WITH LOGIN`)
	if err := attemptLogin(t, loginDSN(t, superDSN, hookReaderRole, pw)); err != nil {
		t.Fatalf("control: login as a LOGIN auth_hook_reader with its password failed: %v", err)
	}

	applyDevBootstrap(t, pool, sql)

	err := attemptLogin(t, loginDSN(t, superDSN, hookReaderRole, pw))
	if err == nil {
		t.Fatalf("%s logged in after bootstrap, want refused (NOLOGIN)", hookReaderRole)
	}
	if code := sqlstate(err); code != "28000" {
		t.Errorf("%s login: SQLSTATE %q (%v), want 28000 (not permitted to log in)", hookReaderRole, code, err)
	}
	if err := attemptLogin(t, loginDSN(t, superDSN, authAdminRole, devDefaultGUCs().authAdmin.value)); err != nil {
		t.Errorf("control: %s login after the same bootstrap failed: %v", authAdminRole, err)
	}
}

func TestBootstrapSQLAuthSchemaIsClosedToEveryOtherRole(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	outsiders := []string{"invoice_app", "invoice_tenant_reader", "invoice_migrator", hookReaderRole}
	t.Cleanup(func() {
		mustExecSQL(t, pool, `REVOKE ALL ON SCHEMA auth FROM PUBLIC, invoice_app, invoice_tenant_reader, invoice_migrator, auth_hook_reader`)
		restoreDevDefaultPasswords(t, pool)
	})
	applyDevBootstrap(t, pool, readBootstrapSQL(t))

	ownerHas := func(role, priv string) bool {
		var ok bool
		if err := pool.QueryRow(context.Background(),
			`SELECT has_schema_privilege($1, 'auth', $2)`, role, priv).Scan(&ok); err != nil {
			t.Fatalf("has_schema_privilege(%s, auth, %s): %v", role, priv, err)
		}
		return ok
	}
	// Control: the owner holds both, so a false below is the grant's absence, not a bad query.
	for _, priv := range []string{"USAGE", "CREATE"} {
		if !ownerHas(authAdminRole, priv) {
			t.Fatalf("control: %s lacks %s on schema auth", authAdminRole, priv)
		}
	}
	for _, role := range outsiders {
		for _, priv := range []string{"USAGE", "CREATE"} {
			if ownerHas(role, priv) {
				t.Errorf("%s has %s on schema auth, want none", role, priv)
			}
		}
	}

	// Behavioural check for the two runtime roles.
	def := devDefaultGUCs()
	for _, tc := range []struct{ role, pw string }{
		{"invoice_app", def.app.value},
		{"invoice_tenant_reader", def.reader.value},
	} {
		conn := connectAs(t, superDSN, tc.role, tc.pw)
		_, err := conn.Exec(context.Background(), `CREATE TABLE auth.qa_probe (id int)`)
		if err == nil {
			t.Errorf("%s created a table in schema auth", tc.role)
			mustExecSQL(t, pool, `DROP TABLE IF EXISTS auth.qa_probe`)
			continue
		}
		if code := sqlstate(err); code != sqlstateInsufficientPrivilege {
			t.Errorf("%s: CREATE TABLE auth.qa_probe: SQLSTATE %q (%v), want %s", tc.role, code, err, sqlstateInsufficientPrivilege)
		}
	}
}

func TestBootstrapSQLAuthAdminHoldsNoPublicObjectPrivilege(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	t.Cleanup(func() {
		mustExecSQL(t, pool, `REVOKE ALL ON ALL TABLES IN SCHEMA public FROM supabase_auth_admin`)
		mustExecSQL(t, pool, `REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM supabase_auth_admin`)
		restoreDevDefaultPasswords(t, pool)
	})
	applyDevBootstrap(t, pool, readBootstrapSQL(t))
	ctx := context.Background()

	var granted []string
	var total int
	rows, err := pool.Query(ctx, `SELECT c.relname,
			CASE WHEN c.relkind = 'S'
				THEN has_sequence_privilege('supabase_auth_admin', c.oid, 'USAGE,SELECT,UPDATE')
				ELSE has_table_privilege('supabase_auth_admin', c.oid, 'SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
			END
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind IN ('r','p','v','m','f','S')`)
	if err != nil {
		t.Fatalf("enumerate public relations: %v", err)
	}
	for rows.Next() {
		var name string
		var has bool
		if err := rows.Scan(&name, &has); err != nil {
			t.Fatalf("scan: %v", err)
		}
		total++
		if has {
			granted = append(granted, name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate public relations: %v", err)
	}
	if total == 0 {
		t.Fatalf("schema public holds no relations, so the privilege scan covers nothing")
	}
	if len(granted) != 0 {
		t.Errorf("%s holds a privilege on %d of %d public relation(s): %v", authAdminRole, len(granted), total, granted)
	}

	// Behavioural check on the cross-tenant table the hook reads.
	aa := connectAs(t, superDSN, authAdminRole, devDefaultGUCs().authAdmin.value)
	if _, err := aa.Exec(ctx, `SELECT 1 FROM public.memberships LIMIT 1`); sqlstate(err) != sqlstateInsufficientPrivilege {
		t.Errorf("%s: SELECT FROM public.memberships: err %v, want SQLSTATE %s", authAdminRole, err, sqlstateInsufficientPrivilege)
	}
}

func TestBootstrapSQLAuthAdminSessionSearchPathIsAuth(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	t.Cleanup(func() { restoreAuthRoles(t, pool) })
	mustExecSQL(t, pool, `ALTER ROLE supabase_auth_admin RESET search_path`)
	applyDevBootstrap(t, pool, readBootstrapSQL(t))

	aa := connectAs(t, superDSN, authAdminRole, devDefaultGUCs().authAdmin.value)
	var sp string
	if err := aa.QueryRow(context.Background(), `SHOW search_path`).Scan(&sp); err != nil {
		t.Fatalf("SHOW search_path as %s: %v", authAdminRole, err)
	}
	if sp != "auth" {
		t.Errorf("%s session search_path = %q, want \"auth\"", authAdminRole, sp)
	}
}

func TestBootstrapSQLAuthHookMembershipConvergesAndStaysSingle(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	sql := readBootstrapSQL(t)
	t.Cleanup(func() {
		mustExecSQL(t, pool, `REVOKE auth_hook_reader FROM invoice_migrator`)
		restoreAuthRoles(t, pool)
		restoreDevDefaultPasswords(t, pool)
	})

	check := func(stage string) {
		t.Helper()
		got := hookReaderMemberships(t, pool)
		if len(got) != 1 {
			t.Fatalf("%s: %d auth_hook_reader -> invoice_migrator membership row(s), want exactly 1", stage, len(got))
		}
		if m := got[0]; m.inherit || !m.set || m.admin {
			t.Errorf("%s: membership inherit=%v set=%v admin=%v, want inherit=false set=true admin=false", stage, m.inherit, m.set, m.admin)
		}
	}

	// From no membership: the grant carries no ADMIN option.
	mustExecSQL(t, pool, `REVOKE auth_hook_reader FROM invoice_migrator`)
	applyDevBootstrap(t, pool, sql)
	check("first run")

	// Re-run over an existing membership: no error, no second row.
	applyDevBootstrap(t, pool, sql)
	check("second run")

	// A drifted membership (INHERIT on, SET off) converges on the next run.
	mustExecSQL(t, pool, `GRANT auth_hook_reader TO invoice_migrator WITH INHERIT TRUE, SET FALSE`)
	if got := hookReaderMemberships(t, pool); len(got) != 1 || !got[0].inherit || got[0].set {
		t.Fatalf("pre-mutation did not drift the membership: %+v", got)
	}
	applyDevBootstrap(t, pool, sql)
	check("after drift")
}

func TestBootstrapSQLDoesNotRotateAuthAdminWhenAnotherGUCIsMissing(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	sql := readBootstrapSQL(t)
	t.Cleanup(func() { restoreDevDefaultPasswords(t, pool) })

	sentinel := "boot-aa-keep-" + uuid.NewString()
	alterRolePassword(t, pool, authAdminRole, sentinel)
	if err := attemptLogin(t, loginDSN(t, superDSN, authAdminRole, sentinel)); err != nil {
		t.Fatalf("sanity: login with the sentinel before the test: %v", err)
	}

	guc := devDefaultGUCs()
	guc.migrator = unsetGUC()
	guc.authAdmin = pwGUC("boot-aa-new-" + uuid.NewString())
	err := applyBootstrap(t, pool, guc, sql)
	if err == nil {
		t.Fatalf("bootstrap with ascomply.migrator_password unset returned nil, want a fail-closed error")
	}
	if err := attemptLogin(t, loginDSN(t, superDSN, authAdminRole, sentinel)); err != nil {
		t.Errorf("%s: sentinel password no longer works, so it was rotated despite a missing GUC: %v", authAdminRole, err)
	}
}
