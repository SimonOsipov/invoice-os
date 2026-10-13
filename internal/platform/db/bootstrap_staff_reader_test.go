package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Names start with TestBootstrapSQL so ci.yml's TestBootstrap filter reaches them.

const staffReaderRole = "staff_reader"

type staffMembership struct{ inherit, set, admin bool }

func staffReaderMemberships(t *testing.T, pool *pgxpool.Pool) []staffMembership {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT m.inherit_option, m.set_option, m.admin_option
		FROM pg_auth_members m
		JOIN pg_roles r ON r.oid = m.roleid JOIN pg_roles u ON u.oid = m.member
		WHERE r.rolname = 'staff_reader' AND u.rolname = 'invoice_migrator'`)
	if err != nil {
		t.Fatalf("read pg_auth_members: %v", err)
	}
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (staffMembership, error) {
		var m staffMembership
		return m, r.Scan(&m.inherit, &m.set, &m.admin)
	})
	if err != nil {
		t.Fatalf("scan pg_auth_members: %v", err)
	}
	return got
}

// ensureStaffReaderForDrift gives a drift test a role to drift when bootstrap has not
// created one yet. A role it created is dropped on cleanup; a pre-existing one is kept.
func ensureStaffReaderForDrift(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if roleExists(t, pool, staffReaderRole) {
		return
	}
	mustExecSQL(t, pool, `CREATE ROLE staff_reader NOLOGIN`)
	t.Cleanup(func() {
		ctx := context.Background()
		for _, stmt := range []string{`DROP OWNED BY staff_reader`, `DROP ROLE staff_reader`} {
			if _, err := pool.Exec(ctx, stmt); err != nil {
				t.Errorf("drop the staff_reader this test created: %s: %v", stmt, err)
			}
		}
	})
}

func TestBootstrapSQLStaffReaderIsNologinWithSafeAttributes(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	t.Cleanup(func() { restoreAuthRoles(t, pool); restoreDevDefaultPasswords(t, pool) })

	applyDevBootstrap(t, pool, readBootstrapSQL(t))

	if !roleExists(t, pool, staffReaderRole) {
		t.Fatalf("role %s not found in pg_roles after bootstrap", staffReaderRole)
	}
	attrs := readRoleAttrs(t, pool, staffReaderRole)
	if attrs.canLogin {
		t.Errorf("%s: rolcanlogin = true, want false (NOLOGIN)", staffReaderRole)
	}
	if attrs.super {
		t.Errorf("%s: rolsuper = true, want false (NOSUPERUSER)", staffReaderRole)
	}
	if attrs.bypassRLS {
		t.Errorf("%s: rolbypassrls = true, want false (NOBYPASSRLS)", staffReaderRole)
	}
	if attrs.createDB {
		t.Errorf("%s: rolcreatedb = true, want false (NOCREATEDB)", staffReaderRole)
	}
	if attrs.createRole {
		t.Errorf("%s: rolcreaterole = true, want false (NOCREATEROLE)", staffReaderRole)
	}
	var hasPassword bool
	if err := pool.QueryRow(context.Background(),
		`SELECT rolpassword IS NOT NULL FROM pg_authid WHERE rolname = $1`, staffReaderRole,
	).Scan(&hasPassword); err != nil {
		t.Fatalf("read pg_authid for %s: %v", staffReaderRole, err)
	}
	if hasPassword {
		t.Errorf("%s has a password, want none", staffReaderRole)
	}
}

func TestBootstrapSQLStaffReaderDriftIsReasserted(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	ensureStaffReaderForDrift(t, pool)
	t.Cleanup(func() { restoreAuthRoles(t, pool); restoreDevDefaultPasswords(t, pool) })

	mustExecSQL(t, pool, `ALTER ROLE staff_reader WITH LOGIN SUPERUSER BYPASSRLS CREATEDB CREATEROLE`)
	if a := readRoleAttrs(t, pool, staffReaderRole); !a.canLogin || !a.super || !a.bypassRLS || !a.createDB || !a.createRole {
		t.Fatalf("pre-mutation did not drift %s: %+v", staffReaderRole, a)
	}

	applyDevBootstrap(t, pool, readBootstrapSQL(t))

	a := readRoleAttrs(t, pool, staffReaderRole)
	if a.canLogin {
		t.Errorf("%s: rolcanlogin = true after bootstrap, want false", staffReaderRole)
	}
	if a.super {
		t.Errorf("%s: rolsuper = true after bootstrap, want false", staffReaderRole)
	}
	if a.bypassRLS {
		t.Errorf("%s: rolbypassrls = true after bootstrap, want false", staffReaderRole)
	}
	if a.createDB {
		t.Errorf("%s: rolcreatedb = true after bootstrap, want false", staffReaderRole)
	}
	if a.createRole {
		t.Errorf("%s: rolcreaterole = true after bootstrap, want false", staffReaderRole)
	}
}

func TestBootstrapSQLStaffReaderCannotLogInEvenWithAPassword(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	sql := readBootstrapSQL(t)
	ensureStaffReaderForDrift(t, pool)
	t.Cleanup(func() {
		mustExecSQL(t, pool, `ALTER ROLE staff_reader WITH NOLOGIN PASSWORD NULL`)
		restoreAuthRoles(t, pool)
		restoreDevDefaultPasswords(t, pool)
	})

	// LOGIN with a known password: only bootstrap's NOLOGIN can refuse the login.
	pw := "staff-login-" + uuid.NewString()
	alterRolePassword(t, pool, staffReaderRole, pw)
	mustExecSQL(t, pool, `ALTER ROLE staff_reader WITH LOGIN`)
	if err := attemptLogin(t, loginDSN(t, superDSN, staffReaderRole, pw)); err != nil {
		t.Fatalf("control: login as a LOGIN staff_reader with its password failed: %v", err)
	}

	applyDevBootstrap(t, pool, sql)
	// Bootstrap clears the password; set it again so only NOLOGIN can refuse.
	alterRolePassword(t, pool, staffReaderRole, pw)

	err := attemptLogin(t, loginDSN(t, superDSN, staffReaderRole, pw))
	if err == nil {
		t.Fatalf("%s logged in after bootstrap, want refused (NOLOGIN)", staffReaderRole)
	}
	if code := sqlstate(err); code != "28000" {
		t.Errorf("%s login: SQLSTATE %q (%v), want 28000 (not permitted to log in)", staffReaderRole, code, err)
	}
	if err := attemptLogin(t, loginDSN(t, superDSN, authAdminRole, devDefaultGUCs().authAdmin.value)); err != nil {
		t.Errorf("control: %s login after the same bootstrap failed: %v", authAdminRole, err)
	}
}

func TestBootstrapSQLStaffReaderMembershipConvergesAndStaysSingle(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	sql := readBootstrapSQL(t)
	t.Cleanup(func() {
		if roleExists(t, pool, staffReaderRole) {
			mustExecSQL(t, pool, `REVOKE staff_reader FROM invoice_migrator`)
		}
		restoreAuthRoles(t, pool)
		restoreDevDefaultPasswords(t, pool)
	})

	check := func(stage string) {
		t.Helper()
		got := staffReaderMemberships(t, pool)
		if len(got) != 1 {
			t.Fatalf("%s: %d staff_reader -> invoice_migrator membership row(s), want exactly 1", stage, len(got))
		}
		if m := got[0]; m.inherit || !m.set || m.admin {
			t.Errorf("%s: membership inherit=%v set=%v admin=%v, want inherit=false set=true admin=false", stage, m.inherit, m.set, m.admin)
		}
	}

	// From no membership: the grant carries no ADMIN option.
	if roleExists(t, pool, staffReaderRole) {
		mustExecSQL(t, pool, `REVOKE staff_reader FROM invoice_migrator`)
	}
	applyDevBootstrap(t, pool, sql)
	check("first run")

	// Re-run over an existing membership: no error, no second row.
	applyDevBootstrap(t, pool, sql)
	check("second run")

	// A drifted membership (INHERIT on, SET off) converges on the next run.
	mustExecSQL(t, pool, `GRANT staff_reader TO invoice_migrator WITH INHERIT TRUE, SET FALSE`)
	if got := staffReaderMemberships(t, pool); len(got) != 1 || !got[0].inherit || got[0].set {
		t.Fatalf("pre-mutation did not drift the membership: %+v", got)
	}
	applyDevBootstrap(t, pool, sql)
	check("after drift")
}

func TestBootstrapSQLStaffReaderIsSetNotInherited(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	t.Cleanup(func() { restoreAuthRoles(t, pool); restoreDevDefaultPasswords(t, pool) })
	applyDevBootstrap(t, pool, readBootstrapSQL(t))
	ctx := context.Background()

	if !roleExists(t, pool, staffReaderRole) {
		t.Fatalf("role %s not found in pg_roles after bootstrap", staffReaderRole)
	}
	mig := connectAs(t, superDSN, "invoice_migrator", devDefaultGUCs().migrator.value)

	var inherits bool
	if err := mig.QueryRow(ctx, `SELECT pg_has_role('invoice_migrator', 'staff_reader', 'USAGE')`).Scan(&inherits); err != nil {
		t.Fatalf("pg_has_role USAGE: %v", err)
	}
	if inherits {
		t.Errorf("invoice_migrator inherits staff_reader (pg_has_role USAGE), want it SET-only")
	}

	// Control: the SET path is what the migration uses to own staff functions.
	if _, err := mig.Exec(ctx, `SET ROLE staff_reader`); err != nil {
		t.Fatalf("invoice_migrator SET ROLE staff_reader: %v", err)
	}
	var cur string
	if err := mig.QueryRow(ctx, `SELECT current_user`).Scan(&cur); err != nil || cur != staffReaderRole {
		t.Fatalf("current_user after SET ROLE = %q (err %v), want %s", cur, err, staffReaderRole)
	}
}

func TestBootstrapSQLStaffReaderSchemaPrivileges(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	sql := readBootstrapSQL(t)
	t.Cleanup(func() { restoreAuthRoles(t, pool); restoreDevDefaultPasswords(t, pool) })

	// Hide PUBLIC's USAGE and strip any earlier grant, so only this run can supply them.
	revokePublicSchemaUsage(t, pool)
	if roleExists(t, pool, staffReaderRole) {
		mustExecSQL(t, pool, `REVOKE USAGE, CREATE ON SCHEMA public FROM staff_reader`)
	}

	applyDevBootstrap(t, pool, sql)

	if !roleExists(t, pool, staffReaderRole) {
		t.Fatalf("role %s not found in pg_roles after bootstrap", staffReaderRole)
	}
	for _, priv := range []string{"USAGE", "CREATE"} {
		if !hasSchemaPrivilege(t, pool, staffReaderRole, priv) {
			t.Errorf("%s lacks %s on schema public", staffReaderRole, priv)
		}
	}
}

func TestBootstrapSQLStaffReaderIsCreatedWhenAbsent(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	sql := readBootstrapSQL(t)
	t.Cleanup(func() {
		applyDevBootstrap(t, pool, sql)
		restoreAuthRoles(t, pool)
		restoreDevDefaultPasswords(t, pool)
	})

	if roleExists(t, pool, staffReaderRole) {
		mustExecSQL(t, pool, `DROP OWNED BY staff_reader`)
		mustExecSQL(t, pool, `DROP ROLE staff_reader`)
	}
	if roleExists(t, pool, staffReaderRole) {
		t.Fatalf("pre-mutation did not drop %s", staffReaderRole)
	}

	applyDevBootstrap(t, pool, sql)

	if !roleExists(t, pool, staffReaderRole) {
		t.Fatalf("role %s not created by bootstrap", staffReaderRole)
	}
	if a := readRoleAttrs(t, pool, staffReaderRole); a.canLogin || a.super || a.bypassRLS || a.createDB || a.createRole {
		t.Errorf("%s created with unsafe attributes: %+v", staffReaderRole, a)
	}
	var hasPassword bool
	if err := pool.QueryRow(context.Background(),
		`SELECT rolpassword IS NOT NULL FROM pg_authid WHERE rolname = $1`, staffReaderRole,
	).Scan(&hasPassword); err != nil {
		t.Fatalf("read pg_authid: %v", err)
	}
	if hasPassword {
		t.Errorf("%s created with a password, want none", staffReaderRole)
	}
	if got := staffReaderMemberships(t, pool); len(got) != 1 || got[0].inherit || !got[0].set || got[0].admin {
		t.Errorf("memberships after create = %+v, want one inherit=false set=true admin=false", got)
	}
	for _, priv := range []string{"USAGE", "CREATE"} {
		if !hasSchemaPrivilege(t, pool, staffReaderRole, priv) {
			t.Errorf("%s lacks %s on schema public after create", staffReaderRole, priv)
		}
	}
}

func TestBootstrapSQLStaffReaderEachAttributeDriftedAloneIsReasserted(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	sql := readBootstrapSQL(t)
	ensureStaffReaderForDrift(t, pool)
	t.Cleanup(func() { restoreAuthRoles(t, pool); restoreDevDefaultPasswords(t, pool) })

	cases := []struct {
		name  string
		drift string
		bad   func(roleAttrs) bool
	}{
		{"LOGIN", "LOGIN", func(a roleAttrs) bool { return a.canLogin }},
		{"SUPERUSER", "SUPERUSER", func(a roleAttrs) bool { return a.super }},
		{"BYPASSRLS", "BYPASSRLS", func(a roleAttrs) bool { return a.bypassRLS }},
		{"CREATEDB", "CREATEDB", func(a roleAttrs) bool { return a.createDB }},
		{"CREATEROLE", "CREATEROLE", func(a roleAttrs) bool { return a.createRole }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mustExecSQL(t, pool, `ALTER ROLE staff_reader WITH NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE`)
			mustExecSQL(t, pool, `ALTER ROLE staff_reader WITH `+c.drift)
			if !c.bad(readRoleAttrs(t, pool, staffReaderRole)) {
				t.Fatalf("pre-mutation did not drift %s", c.name)
			}
			applyDevBootstrap(t, pool, sql)
			if a := readRoleAttrs(t, pool, staffReaderRole); a.canLogin || a.super || a.bypassRLS || a.createDB || a.createRole {
				t.Errorf("%s drift not reasserted: %+v", c.name, a)
			}
		})
	}
}

func TestBootstrapSQLStaffReaderSchemaPrivilegeDriftedAloneIsReasserted(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	sql := readBootstrapSQL(t)
	ensureStaffReaderForDrift(t, pool)
	t.Cleanup(func() { restoreAuthRoles(t, pool); restoreDevDefaultPasswords(t, pool) })
	revokePublicSchemaUsage(t, pool)

	for _, priv := range []string{"USAGE", "CREATE"} {
		t.Run(priv, func(t *testing.T) {
			mustExecSQL(t, pool, `GRANT USAGE, CREATE ON SCHEMA public TO staff_reader`)
			mustExecSQL(t, pool, `REVOKE `+priv+` ON SCHEMA public FROM staff_reader`)
			if hasSchemaPrivilege(t, pool, staffReaderRole, priv) {
				t.Fatalf("pre-mutation left %s on schema public", priv)
			}
			applyDevBootstrap(t, pool, sql)
			for _, p := range []string{"USAGE", "CREATE"} {
				if !hasSchemaPrivilege(t, pool, staffReaderRole, p) {
					t.Errorf("%s lacks %s on schema public after bootstrap", staffReaderRole, p)
				}
			}
		})
	}
}

func TestBootstrapSQLStaffReaderAdminOptionDriftConverges(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	sql := readBootstrapSQL(t)
	ensureStaffReaderForDrift(t, pool)
	t.Cleanup(func() {
		mustExecSQL(t, pool, `REVOKE staff_reader FROM invoice_migrator`)
		restoreAuthRoles(t, pool)
		restoreDevDefaultPasswords(t, pool)
	})

	mustExecSQL(t, pool, `GRANT staff_reader TO invoice_migrator WITH ADMIN TRUE, INHERIT FALSE, SET TRUE`)
	if got := staffReaderMemberships(t, pool); len(got) != 1 || !got[0].admin {
		t.Fatalf("pre-mutation did not drift ADMIN: %+v", got)
	}

	applyDevBootstrap(t, pool, sql)

	got := staffReaderMemberships(t, pool)
	if len(got) != 1 {
		t.Fatalf("%d membership rows after bootstrap, want 1", len(got))
	}
	if got[0].admin {
		t.Errorf("membership admin=true after bootstrap, want false")
	}
}

func TestBootstrapSQLStaffReaderDriftedPasswordIsCleared(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	sql := readBootstrapSQL(t)
	ensureStaffReaderForDrift(t, pool)
	t.Cleanup(func() {
		mustExecSQL(t, pool, `ALTER ROLE staff_reader PASSWORD NULL`)
		restoreAuthRoles(t, pool)
		restoreDevDefaultPasswords(t, pool)
	})

	alterRolePassword(t, pool, staffReaderRole, "staff-drift-"+uuid.NewString())
	var hasPw bool
	if err := pool.QueryRow(context.Background(), `SELECT rolpassword IS NOT NULL FROM pg_authid WHERE rolname = 'staff_reader'`).Scan(&hasPw); err != nil || !hasPw {
		t.Fatalf("pre-mutation did not set a password: has=%v err=%v", hasPw, err)
	}

	applyDevBootstrap(t, pool, sql)

	if err := pool.QueryRow(context.Background(), `SELECT rolpassword IS NOT NULL FROM pg_authid WHERE rolname = 'staff_reader'`).Scan(&hasPw); err != nil {
		t.Fatalf("read pg_authid: %v", err)
	}
	if hasPw {
		t.Errorf("staff_reader holds a password after bootstrap, want none")
	}
}
