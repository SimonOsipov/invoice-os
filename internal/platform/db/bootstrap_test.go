// [M4-21-02] Test-first (RED) suite for the pgx-callable rewrite of
// `db/bootstrap.sql`. Written BEFORE that rewrite exists (Test-first: yes) — the
// checked-in file still uses psql-only `\set` meta-commands and `:'var'` client-side
// interpolation (db/bootstrap.sql:25,53-55), which pgx CANNOT execute at all: sent
// verbatim over the wire, `\set ON_ERROR_STOP on` is not valid SQL and the whole batch
// is rejected with a syntax error before a single statement runs. So EVERY test below
// is expected to fail at its first applyBootstrap call — that failure IS the RED state
// this file proves; [M4-21-02]'s execution stage rewrites the file to read three
// `fiscalbridge.*` session GUCs (Decision `[one-bootstrap-file]`) and these tests turn
// green with no changes needed here, other than removing this paragraph.
//
// Design, mirroring this package's existing conventions:
//   - Env-gated skip on DATABASE_SUPERUSER_URL only (bootstrap.sql runs exclusively as
//     the superuser) — a bare `go test ./...` and the default CI `go` job stay green
//     with no database. Self-contained: does NOT depend on the shared RLS harness
//     (rls_harness_test.go) — like migrate_test.go, it opens its own pool.
//   - The dev/CI Postgres this suite runs against (`make dev-db`, or the CI service
//     container) already has the three real roles bootstrapped and OWNING the entire
//     migrated schema (invoice_migrator owns every table). Forcibly dropping them to
//     satisfy a literal "roles absent" precondition would destroy that shared schema
//     out from under every other test in this package — instead, TestBootstrapSQL-
//     CreatesRolesWithGivenPasswords exercises the create-or-converge path bootstrap.sql
//     itself is idempotent over, which is exactly what runs the FIRST time in a genuinely
//     fresh CI container.
//   - Every test mutates the SAME three shared roles, so none of them use t.Parallel()
//     (matches demo_reset_test.go's rationale for shared global state), and every test
//     registers a t.Cleanup that restores passwords/attributes to the dev/CI baseline
//     BEFORE it mutates anything — so a RED failure (or any panic) never leaves the
//     shared roles rotated/elevated for the rest of the package's test run. Restoration
//     never goes through db/bootstrap.sql itself (that would depend on the very file
//     under test); it uses direct ALTER ROLE statements instead.
package db_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// bootstrapSQLPath is db/bootstrap.sql relative to this package directory
// (internal/platform/db), i.e. the repo root's db/bootstrap.sql.
const bootstrapSQLPath = "../../../db/bootstrap.sql"

// bootstrapRoles are the three roles db/bootstrap.sql creates/re-asserts, in the order
// the file itself declares them.
var bootstrapRoles = []string{"invoice_migrator", "invoice_app", "invoice_tenant_reader"}

// gucValue distinguishes "leave this session GUC unset" (current_setting(name, true)
// then reads NULL) from "explicitly set it to the empty string" — the Test Spec's two
// distinct fail-closed scenarios (GUCs unset, or set to an empty string). The zero
// value is unset.
type gucValue struct {
	set   bool
	value string
}

func unsetGUC() gucValue      { return gucValue{} }
func emptyGUC() gucValue      { return gucValue{set: true, value: ""} }
func pwGUC(v string) gucValue { return gucValue{set: true, value: v} }

// bootstrapGUCs bundles the three fiscalbridge.*_password session GUCs a pgx caller
// (or the Makefile's `-c "SELECT set_config(...)" -f` psql invocation) must set before
// running db/bootstrap.sql.
type bootstrapGUCs struct {
	migrator, app, reader gucValue
}

// devDefaultGUCs returns the three password values matching every CI job's and the
// Makefile's own dev-bootstrap defaults (MIGRATOR_PASSWORD/APP_PASSWORD/
// READER_PASSWORD, `?= migrator/app/reader`; see .github/workflows/ci.yml and
// Makefile). Env override honored, so a customized .env dev DB restores to ITS
// defaults, not a hardcoded stranger value.
func devDefaultGUCs() bootstrapGUCs {
	return bootstrapGUCs{
		migrator: pwGUC(envOr("MIGRATOR_PASSWORD", "migrator")),
		app:      pwGUC(envOr("APP_PASSWORD", "app")),
		reader:   pwGUC(envOr("READER_PASSWORD", "reader")),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// requireSuperuserDSN skips the calling test when DATABASE_SUPERUSER_URL is not set.
func requireSuperuserDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("DATABASE_SUPERUSER_URL")
	if dsn == "" {
		t.Skip("DATABASE_SUPERUSER_URL not set; skipping db/bootstrap.sql pgx-caller test")
	}
	return dsn
}

// bootstrapSuperuserPool opens a pool against dsn and closes it on test cleanup.
func bootstrapSuperuserPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open superuser pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// readBootstrapSQL reads db/bootstrap.sql from disk — the exact file both psql and the
// future pgx caller run, never a copy (Decision [one-bootstrap-file]).
func readBootstrapSQL(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(bootstrapSQLPath)
	if err != nil {
		t.Fatalf("read %s: %v", bootstrapSQLPath, err)
	}
	return string(b)
}

// applyBootstrap runs db/bootstrap.sql over ONE acquired connection: the three
// fiscalbridge.* GUCs set (session-scoped, is_local=false — matching the Makefile's
// `-c "SELECT set_config(..., false)" -f` precedent) on that connection first, then the
// file executed as a single zero-arg Exec so pgx uses the simple query protocol its
// multi-statement DO $$ … $$ body requires (same requirement as db/demo-reset.sql, see
// demo_reset_test.go's applyDemoReset). Using one acquired connection (not the pool
// directly) is load-bearing: two separate pool.Exec calls could land on two different
// physical connections, and a GUC set on one would never be visible to the other.
func applyBootstrap(t *testing.T, pool *pgxpool.Pool, guc bootstrapGUCs, sql string) error {
	t.Helper()
	ctx := context.Background()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer conn.Release()

	for _, kv := range []struct {
		name string
		v    gucValue
	}{
		{"fiscalbridge.migrator_password", guc.migrator},
		{"fiscalbridge.app_password", guc.app},
		{"fiscalbridge.reader_password", guc.reader},
	} {
		if !kv.v.set {
			continue // leave unset: current_setting(name, true) then reads NULL.
		}
		if _, err := conn.Exec(ctx, `SELECT set_config($1, $2, false)`, kv.name, kv.v.value); err != nil {
			t.Fatalf("set_config(%s): %v", kv.name, err)
		}
	}

	_, err = conn.Exec(ctx, sql)
	return err
}

// alterRolePassword sets role's password directly via ALTER ROLE, bypassing
// db/bootstrap.sql entirely — used both to plant a known sentinel/baseline password
// and to restore the dev/CI default afterward, so neither depends on the file under
// test. ALTER ROLE ... PASSWORD requires a string literal, not a bind parameter (the
// same grammar constraint bootstrap.sql itself works around via EXECUTE format('%L') —
// see the story's Decision), so the value is dollar-quoted with a fixed tag. Safe here
// because every caller passes a Go-generated value (uuid-suffixed or a fixed sentinel
// word), never external input, and none contains "$pw$".
func alterRolePassword(t *testing.T, pool *pgxpool.Pool, role, password string) {
	t.Helper()
	if strings.Contains(password, "$pw$") {
		t.Fatalf("test bug: password contains the dollar-quote tag $pw$: %q", password)
	}
	stmt := fmt.Sprintf(`ALTER ROLE %s PASSWORD $pw$%s$pw$`, role, password)
	if _, err := pool.Exec(context.Background(), stmt); err != nil {
		t.Fatalf("ALTER ROLE %s PASSWORD: %v", role, err)
	}
}

// restoreDevDefaultPasswords sets all three roles' passwords back to the shared dev/CI
// default directly (not via db/bootstrap.sql). Later tests in this package dial
// DATABASE_MIGRATION_URL / DATABASE_URL / DATABASE_READER_URL built from these exact
// defaults (see Makefile), so this must run in t.Cleanup after any test in this file
// rotates a password.
func restoreDevDefaultPasswords(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	def := devDefaultGUCs()
	for _, kv := range []struct{ role, password string }{
		{"invoice_migrator", def.migrator.value},
		{"invoice_app", def.app.value},
		{"invoice_tenant_reader", def.reader.value},
	} {
		alterRolePassword(t, pool, kv.role, kv.password)
	}
}

// restoreSafeAttributes re-asserts the exact safe attribute set db/bootstrap.sql itself
// asserts on every run, directly (not via the file under test) — used in t.Cleanup so a
// test that deliberately grants BYPASSRLS/SUPERUSER/etc. to probe the fail-closed/
// idempotency path never leaves a role elevated for the rest of the suite (the RLS
// harness's owner-bypass cases depend on invoice_migrator being NOBYPASSRLS).
func restoreSafeAttributes(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	for _, role := range bootstrapRoles {
		if _, err := pool.Exec(ctx,
			`ALTER ROLE `+role+` WITH LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE`,
		); err != nil {
			t.Errorf("restore safe attributes on %s: %v", role, err)
		}
	}
}

// roleAttrs is the security-relevant attribute set db/bootstrap.sql must assert on
// every run (its step 2).
type roleAttrs struct {
	super, bypassRLS, createDB, createRole, canLogin bool
}

func readRoleAttrs(t *testing.T, pool *pgxpool.Pool, role string) roleAttrs {
	t.Helper()
	var a roleAttrs
	err := pool.QueryRow(context.Background(),
		`SELECT rolsuper, rolbypassrls, rolcreatedb, rolcreaterole, rolcanlogin
		   FROM pg_roles WHERE rolname = $1`,
		role,
	).Scan(&a.super, &a.bypassRLS, &a.createDB, &a.createRole, &a.canLogin)
	if err != nil {
		t.Fatalf("read pg_roles for %s (does the role exist?): %v", role, err)
	}
	return a
}

// loginDSN builds a connection string for role/password against the same
// host/port/dbname/sslmode as superuserDSN, by swapping only the credentials.
func loginDSN(t *testing.T, superuserDSN, role, password string) string {
	t.Helper()
	u, err := url.Parse(superuserDSN)
	if err != nil {
		t.Fatalf("parse superuser dsn: %v", err)
	}
	u.User = url.UserPassword(role, password)
	return u.String()
}

// attemptLogin opens exactly one NEW physical connection as dsn's role/password and
// immediately closes it. It is deliberately a fresh connection (never a pooled/reused
// one) since only a new login round-trip actually authenticates against the role's
// CURRENT password.
func attemptLogin(t *testing.T, dsn string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	return conn.Ping(ctx)
}

// hasSchemaPrivilege reports has_schema_privilege(role, 'public', priv). Passing the
// literal role name "public" checks the PUBLIC pseudo-role (Postgres special-cases it
// in every has_*_privilege function), which is how TestBootstrapSQLGrantsSchema-
// Privileges asserts PUBLIC's CREATE was revoked.
func hasSchemaPrivilege(t *testing.T, pool *pgxpool.Pool, role, priv string) bool {
	t.Helper()
	var ok bool
	if err := pool.QueryRow(context.Background(),
		`SELECT has_schema_privilege($1, 'public', $2)`, role, priv,
	).Scan(&ok); err != nil {
		t.Fatalf("has_schema_privilege(%s, public, %s): %v", role, priv, err)
	}
	return ok
}

// TestBootstrapSQLCreatesRolesWithGivenPasswords: Test Spec row 1 / Core AC-2. Sets the
// three GUCs to unique per-run passwords and applies db/bootstrap.sql from disk; all
// three roles must exist (LOGIN) and each must accept a NEW connection authenticated
// with EXACTLY the password its GUC carried — verified by an actual login, not by
// reading a catalog (a catalog only proves a password hash was set, not which one).
func TestBootstrapSQLCreatesRolesWithGivenPasswords(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	sql := readBootstrapSQL(t)
	t.Cleanup(func() { restoreDevDefaultPasswords(t, pool) })

	guc := bootstrapGUCs{
		migrator: pwGUC("boot-mig-" + uuid.NewString()),
		app:      pwGUC("boot-app-" + uuid.NewString()),
		reader:   pwGUC("boot-rdr-" + uuid.NewString()),
	}

	if err := applyBootstrap(t, pool, guc, sql); err != nil {
		t.Fatalf("apply db/bootstrap.sql: %v", err)
	}

	for _, tc := range []struct{ role, password string }{
		{"invoice_migrator", guc.migrator.value},
		{"invoice_app", guc.app.value},
		{"invoice_tenant_reader", guc.reader.value},
	} {
		attrs := readRoleAttrs(t, pool, tc.role)
		if !attrs.canLogin {
			t.Errorf("%s: rolcanlogin = false, want true", tc.role)
		}
		if err := attemptLogin(t, loginDSN(t, superDSN, tc.role, tc.password)); err != nil {
			t.Errorf("%s: login with the GUC-injected password failed: %v", tc.role, err)
		}
	}
}

// TestBootstrapSQLAssertsSecurityAttributes: Test Spec row 2 / Core AC-2. Pre-mutates
// all three roles to the WRONG attributes (SUPERUSER, BYPASSRLS, CREATEDB, CREATEROLE
// all granted) then applies db/bootstrap.sql; every role must come back exactly
// NOSUPERUSER, NOBYPASSRLS, NOCREATEDB, NOCREATEROLE, LOGIN — the attribute
// re-assertion db/bootstrap.sql's step 2 performs unconditionally on every run.
func TestBootstrapSQLAssertsSecurityAttributes(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	sql := readBootstrapSQL(t)

	// Registered before the mutation below: this must fire even if applyBootstrap
	// fails (the RED state), or the shared dev/CI roles would stay elevated for the
	// rest of the package's test run.
	t.Cleanup(func() { restoreSafeAttributes(t, pool) })

	ctx := context.Background()
	for _, role := range bootstrapRoles {
		if _, err := pool.Exec(ctx,
			`ALTER ROLE `+role+` WITH SUPERUSER BYPASSRLS CREATEDB CREATEROLE`,
		); err != nil {
			t.Fatalf("pre-mutate %s to the wrong attributes: %v", role, err)
		}
	}

	if err := applyBootstrap(t, pool, devDefaultGUCs(), sql); err != nil {
		t.Fatalf("apply db/bootstrap.sql: %v", err)
	}

	for _, role := range bootstrapRoles {
		attrs := readRoleAttrs(t, pool, role)
		if attrs.super {
			t.Errorf("%s: rolsuper = true, want false (NOSUPERUSER)", role)
		}
		if attrs.bypassRLS {
			t.Errorf("%s: rolbypassrls = true, want false (NOBYPASSRLS)", role)
		}
		if attrs.createDB {
			t.Errorf("%s: rolcreatedb = true, want false (NOCREATEDB)", role)
		}
		if attrs.createRole {
			t.Errorf("%s: rolcreaterole = true, want false (NOCREATEROLE)", role)
		}
		if !attrs.canLogin {
			t.Errorf("%s: rolcanlogin = false, want true (LOGIN)", role)
		}
	}
}

// TestBootstrapSQLFailsClosedOnMissingPassword: Test Spec row 3 / Core AC-3. Covers
// both wordings of the Given (GUCs unset, or set to an empty string): a subtest where
// the three GUCs are never set at all, and one where they are explicitly set to the
// empty string. Either way, applying db/bootstrap.sql must return an error naming the
// missing setting, and — checked via an actual login against a known sentinel
// password planted beforehand, not a catalog read — no role's password may have
// changed.
func TestBootstrapSQLFailsClosedOnMissingPassword(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	sql := readBootstrapSQL(t)

	sentinels := map[string]string{
		"invoice_migrator":      "boot-sentinel-mig",
		"invoice_app":           "boot-sentinel-app",
		"invoice_tenant_reader": "boot-sentinel-rdr",
	}
	for role, pw := range sentinels {
		alterRolePassword(t, pool, role, pw)
	}
	t.Cleanup(func() { restoreDevDefaultPasswords(t, pool) })
	for role, pw := range sentinels {
		if err := attemptLogin(t, loginDSN(t, superDSN, role, pw)); err != nil {
			t.Fatalf("sanity: login as %s with its sentinel password before the test: %v", role, err)
		}
	}

	for _, tc := range []struct {
		name string
		guc  bootstrapGUCs
	}{
		{"unset", bootstrapGUCs{migrator: unsetGUC(), app: unsetGUC(), reader: unsetGUC()}},
		{"empty string", bootstrapGUCs{migrator: emptyGUC(), app: emptyGUC(), reader: emptyGUC()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := applyBootstrap(t, pool, tc.guc, sql)
			if err == nil {
				t.Fatalf("apply db/bootstrap.sql with %s GUCs: got nil error, want a fail-closed error naming the missing setting", tc.name)
			}
			if !strings.Contains(err.Error(), "fiscalbridge.migrator_password") {
				t.Errorf("error = %q, want it to name fiscalbridge.migrator_password as the missing setting", err.Error())
			}
			for role, pw := range sentinels {
				if loginErr := attemptLogin(t, loginDSN(t, superDSN, role, pw)); loginErr != nil {
					t.Errorf("%s: its sentinel password no longer works after the failed run — a PASSWORD was applied despite the missing GUC: %v", role, loginErr)
				}
			}
		})
	}
}

// TestBootstrapSQLIsIdempotent: Test Spec row 4 / Core AC-4. Applies db/bootstrap.sql
// twice in a row with the SAME GUCs; the second run must succeed with no error, leave
// attributes/grants unchanged, and logins must still succeed with the same password.
func TestBootstrapSQLIsIdempotent(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	sql := readBootstrapSQL(t)
	t.Cleanup(func() { restoreDevDefaultPasswords(t, pool); restoreSafeAttributes(t, pool) })

	guc := bootstrapGUCs{
		migrator: pwGUC("boot-idem-mig-" + uuid.NewString()),
		app:      pwGUC("boot-idem-app-" + uuid.NewString()),
		reader:   pwGUC("boot-idem-rdr-" + uuid.NewString()),
	}

	if err := applyBootstrap(t, pool, guc, sql); err != nil {
		t.Fatalf("first apply of db/bootstrap.sql: %v", err)
	}
	if err := applyBootstrap(t, pool, guc, sql); err != nil {
		t.Fatalf("second apply of db/bootstrap.sql (idempotency): %v", err)
	}

	for _, tc := range []struct{ role, password string }{
		{"invoice_migrator", guc.migrator.value},
		{"invoice_app", guc.app.value},
		{"invoice_tenant_reader", guc.reader.value},
	} {
		attrs := readRoleAttrs(t, pool, tc.role)
		if attrs.super || attrs.bypassRLS || attrs.createDB || attrs.createRole || !attrs.canLogin {
			t.Errorf("%s: attributes drifted after the second (idempotent) apply: %+v", tc.role, attrs)
		}
		if err := attemptLogin(t, loginDSN(t, superDSN, tc.role, tc.password)); err != nil {
			t.Errorf("%s: login after the second apply failed: %v", tc.role, err)
		}
	}

	if !hasSchemaPrivilege(t, pool, "invoice_migrator", "USAGE") || !hasSchemaPrivilege(t, pool, "invoice_migrator", "CREATE") {
		t.Errorf("invoice_migrator schema grants missing after the second (idempotent) apply")
	}
	if !hasSchemaPrivilege(t, pool, "invoice_tenant_reader", "USAGE") {
		t.Errorf("invoice_tenant_reader schema USAGE missing after the second (idempotent) apply")
	}
}

// TestBootstrapSQLRotatesPasswordDeterministically: Test Spec row 5 / Core AC-4.
// Bootstraps with password p1, then re-bootstraps with p2; a login with p2 must
// succeed and a login with p1 must now be refused — proving the rotation actually
// took effect rather than db/bootstrap.sql silently no-op'ing on an existing role.
func TestBootstrapSQLRotatesPasswordDeterministically(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	sql := readBootstrapSQL(t)
	t.Cleanup(func() { restoreDevDefaultPasswords(t, pool) })

	p1 := "boot-rot-p1-" + uuid.NewString()
	p2 := "boot-rot-p2-" + uuid.NewString()

	if err := applyBootstrap(t, pool, bootstrapGUCs{migrator: pwGUC(p1), app: pwGUC(p1), reader: pwGUC(p1)}, sql); err != nil {
		t.Fatalf("bootstrap with p1: %v", err)
	}
	if err := attemptLogin(t, loginDSN(t, superDSN, "invoice_migrator", p1)); err != nil {
		t.Fatalf("sanity: login with p1 right after bootstrapping with p1: %v", err)
	}

	if err := applyBootstrap(t, pool, bootstrapGUCs{migrator: pwGUC(p2), app: pwGUC(p2), reader: pwGUC(p2)}, sql); err != nil {
		t.Fatalf("re-bootstrap with p2: %v", err)
	}

	if err := attemptLogin(t, loginDSN(t, superDSN, "invoice_migrator", p2)); err != nil {
		t.Errorf("login with the new password p2 failed: %v", err)
	}
	if err := attemptLogin(t, loginDSN(t, superDSN, "invoice_migrator", p1)); err == nil {
		t.Errorf("login with the OLD password p1 still succeeded after rotation to p2 — password was not actually rotated")
	}
}

// TestBootstrapSQLGrantsSchemaPrivileges: Test Spec row 6 / Core AC-2. Applies
// db/bootstrap.sql to a DB whose grants are at their default state, then asserts the
// exact schema-level privilege shape its step 4 sets: invoice_migrator gets
// USAGE+CREATE on public, invoice_tenant_reader gets USAGE only, and PUBLIC's ambient
// CREATE on public is revoked.
func TestBootstrapSQLGrantsSchemaPrivileges(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	sql := readBootstrapSQL(t)
	t.Cleanup(func() { restoreDevDefaultPasswords(t, pool) })

	if err := applyBootstrap(t, pool, devDefaultGUCs(), sql); err != nil {
		t.Fatalf("apply db/bootstrap.sql: %v", err)
	}

	if !hasSchemaPrivilege(t, pool, "invoice_migrator", "USAGE") {
		t.Errorf("invoice_migrator lacks USAGE on schema public")
	}
	if !hasSchemaPrivilege(t, pool, "invoice_migrator", "CREATE") {
		t.Errorf("invoice_migrator lacks CREATE on schema public")
	}
	if !hasSchemaPrivilege(t, pool, "invoice_tenant_reader", "USAGE") {
		t.Errorf("invoice_tenant_reader lacks USAGE on schema public")
	}
	if hasSchemaPrivilege(t, pool, "public", "CREATE") {
		t.Errorf("PUBLIC pseudo-role has CREATE on schema public, want it revoked")
	}
}
