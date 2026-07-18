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
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	dbsql "github.com/SimonOsipov/invoice-os/db"
	db "github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
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

	// Registered before the pre-mutation below (mirrors
	// TestBootstrapSQLAssertsSecurityAttributes's ordering): must fire even if a
	// REVOKE/GRANT statement here itself fails, or the shared dev/CI roles would be
	// left without their normal schema grants for the rest of the package's test run.
	t.Cleanup(func() {
		ctx := context.Background()
		if _, err := pool.Exec(ctx, `GRANT USAGE, CREATE ON SCHEMA public TO invoice_migrator`); err != nil {
			t.Errorf("restore invoice_migrator schema grants: %v", err)
		}
		if _, err := pool.Exec(ctx, `GRANT USAGE ON SCHEMA public TO invoice_tenant_reader`); err != nil {
			t.Errorf("restore invoice_tenant_reader schema grants: %v", err)
		}
		if _, err := pool.Exec(ctx, `REVOKE CREATE ON SCHEMA public FROM PUBLIC`); err != nil {
			t.Errorf("restore PUBLIC schema grants: %v", err)
		}
	})

	// Pre-mutate every grant this test asserts to its WRONG state first. Without
	// this, the assertions below would pass unconditionally: the shared dev/CI DB
	// already carries these exact grants from an earlier bootstrap run (this job's
	// own preceding `make db-bootstrap` step in CI, or a prior `make dev-db`
	// locally) — proven by mutation testing (deleting every GRANT/REVOKE line from
	// db/bootstrap.sql's step 4 left this test green, because it was reading grants
	// nobody in the test run itself had ever applied).
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `REVOKE USAGE, CREATE ON SCHEMA public FROM invoice_migrator`); err != nil {
		t.Fatalf("pre-mutate: revoke invoice_migrator schema grants: %v", err)
	}
	if _, err := pool.Exec(ctx, `REVOKE USAGE ON SCHEMA public FROM invoice_tenant_reader`); err != nil {
		t.Fatalf("pre-mutate: revoke invoice_tenant_reader schema grants: %v", err)
	}
	if _, err := pool.Exec(ctx, `GRANT CREATE ON SCHEMA public TO PUBLIC`); err != nil {
		t.Fatalf("pre-mutate: grant PUBLIC CREATE on schema public: %v", err)
	}

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

// ---- QA adversarial coverage (post-implementation, Mode B) -----------------------
//
// The tests below were NOT part of the architect's pre-authored Test Specs table;
// they were added during QA verification of task-126 to close gaps mutation testing
// found or to lock in behavior the Test Specs never exercised: %L-quoting safety
// against injection-shaped passwords, the actual (accepted, not rejected) handling
// of a whitespace-only GUC, safety under concurrent/repeated invocation, and a
// static regression guard for the highest-risk fix in this subtask (psql silently
// no-op'ing on redirected stdin once a `-c` flag is present).

// TestBootstrapSQLPasswordSpecialCharactersRoundTrip: adversarial coverage for the
// %L (literal-quoting) EXECUTE format(...) path db/bootstrap.sql's step 3 uses to
// apply passwords. A password containing a single quote, a backslash, an embedded
// SQL-injection-shaped payload aimed at breaking out of the %L literal (e.g. to
// append `GRANT SUPERUSER`), and a value that collides with the dollar-quote tag
// this test file's own alterRolePassword helper uses elsewhere, must all round-trip
// EXACTLY (login succeeds with the literal string) and must never grant SUPERUSER —
// proving %L quoting, not string concatenation, is what's actually happening.
func TestBootstrapSQLPasswordSpecialCharactersRoundTrip(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	sql := readBootstrapSQL(t)
	t.Cleanup(func() { restoreDevDefaultPasswords(t, pool); restoreSafeAttributes(t, pool) })

	for _, tc := range []struct {
		name string
		pw   string
	}{
		{"single_quote", `boot-o'brien-` + uuid.NewString()},
		{"backslash", `boot-back\slash\pw-` + uuid.NewString()},
		{"percent_L_breakout_attempt", `x', SUPERUSER); RAISE NOTICE 'pwned` + uuid.NewString()},
		{"dollar_quote_tag_collision", `pw$pw$injected-` + uuid.NewString()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guc := bootstrapGUCs{migrator: pwGUC(tc.pw), app: pwGUC(tc.pw), reader: pwGUC(tc.pw)}
			if err := applyBootstrap(t, pool, guc, sql); err != nil {
				t.Fatalf("apply db/bootstrap.sql with a %s password: %v", tc.name, err)
			}
			if err := attemptLogin(t, loginDSN(t, superDSN, "invoice_migrator", tc.pw)); err != nil {
				t.Errorf("login with the exact %s password failed — %%L quoting did not round-trip it: %v", tc.name, err)
			}
			attrs := readRoleAttrs(t, pool, "invoice_migrator")
			if attrs.super {
				t.Errorf("invoice_migrator has rolsuper = true after a %s password — an injection payload escaped the %%L literal", tc.name)
			}
		})
	}
}

// TestBootstrapSQLWhitespaceOnlyGUCIsAcceptedNotRejected documents ACTUAL behavior
// (not asserted by any Test Spec row): db/bootstrap.sql's fail-closed check is
// `coalesce(current_setting(name, true), ”) = ”`, which does NOT trim whitespace.
// A GUC explicitly set to a whitespace-only string is therefore NOT considered
// missing/empty — it is silently accepted as a valid password, unlike an actual
// empty string (covered by TestBootstrapSQLFailsClosedOnMissingPassword). This is a
// real, mutation-confirmed gap in the fail-closed check's coverage (a whitespace GUC
// from a misconfigured env/CI substitution — e.g. `MIGRATOR_PASSWORD=" "` — would
// silently produce a valid-but-useless password instead of the loud failure AC-3
// intends for a blank one); flagged to the executor/architect as a candidate for a
// stricter `btrim(...) = ”` check, not fixed here since it's a production-code
// change outside QA's remit.
func TestBootstrapSQLWhitespaceOnlyGUCIsAcceptedNotRejected(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	sql := readBootstrapSQL(t)
	t.Cleanup(func() { restoreDevDefaultPasswords(t, pool) })

	const whitespacePW = "   "
	guc := bootstrapGUCs{migrator: pwGUC(whitespacePW), app: pwGUC("app"), reader: pwGUC("reader")}

	err := applyBootstrap(t, pool, guc, sql)
	if err != nil {
		t.Fatalf("apply db/bootstrap.sql with a whitespace-only migrator GUC returned an error (documenting current behavior — if this now fails closed, update this test's expectation and its comment): %v", err)
	}
	if loginErr := attemptLogin(t, loginDSN(t, superDSN, "invoice_migrator", whitespacePW)); loginErr != nil {
		t.Errorf("documented current behavior changed: login with the whitespace-only password no longer succeeds: %v", loginErr)
	}
}

// TestBootstrapSQLConcurrentInvocationConverges: adversarial coverage for
// concurrent/repeated invocation (e.g. two CI jobs or two operators accidentally
// running bootstrap against the same DB at once). Three goroutines apply
// db/bootstrap.sql concurrently — each on its OWN acquired connection, with the
// SAME GUC values set on that connection.
//
// A transient race IS tolerated (logged, not failed): empirically, concurrent
// ALTER ROLE/GRANT/REVOKE on the same catalog rows can raise Postgres's "tuple
// concurrently updated" (SQLSTATE XX000) — a non-MVCC catalog-update conflict, not
// a hang or silent corruption. This is PRECISELY why serializing invocations is the
// Go caller's job (M4-21-03's advisory lock, Decision [advisory-lock-on-bootstrap])
// and deliberately not this single-session-oriented file's — so this test documents
// that limitation rather than asserting a guarantee this subtask never promised.
// What MUST hold regardless: no corruption, and one final serial re-apply (the
// realistic recovery/retry a caller would do) converges cleanly.
//
// (Deliberately does NOT call the shared applyBootstrap/t.Fatalf helpers from
// inside the goroutines: testing.T's Fatal/FailNow family may only be called from
// the test's own goroutine, never from another goroutine it spawns.)
func TestBootstrapSQLConcurrentInvocationConverges(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	sql := readBootstrapSQL(t)
	t.Cleanup(func() { restoreDevDefaultPasswords(t, pool); restoreSafeAttributes(t, pool) })
	ctx := context.Background()

	guc := bootstrapGUCs{
		migrator: pwGUC("boot-conc-mig-" + uuid.NewString()),
		app:      pwGUC("boot-conc-app-" + uuid.NewString()),
		reader:   pwGUC("boot-conc-rdr-" + uuid.NewString()),
	}

	const n = 3
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := pool.Acquire(ctx)
			if err != nil {
				errs <- fmt.Errorf("acquire connection: %w", err)
				return
			}
			defer conn.Release()
			for _, kv := range []struct{ name, value string }{
				{"fiscalbridge.migrator_password", guc.migrator.value},
				{"fiscalbridge.app_password", guc.app.value},
				{"fiscalbridge.reader_password", guc.reader.value},
			} {
				if _, err := conn.Exec(ctx, `SELECT set_config($1, $2, false)`, kv.name, kv.value); err != nil {
					errs <- fmt.Errorf("set_config(%s): %w", kv.name, err)
					return
				}
			}
			_, err = conn.Exec(ctx, sql)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)

	raced := 0
	for err := range errs {
		if err != nil {
			raced++
			t.Logf("concurrent apply of db/bootstrap.sql raced (tolerated — see the advisory-lock note above): %v", err)
		}
	}
	if raced == n {
		t.Logf("all %d concurrent invocations raced each other; falling through to the serial re-apply to force convergence", n)
	}

	// The realistic recovery path: one more serial (non-concurrent) apply with the
	// SAME GUCs. This must always succeed and must be what the convergence
	// assertions below are checked against — it is the caller's actual guarantee,
	// not "concurrent invocation is safe on its own".
	if err := applyBootstrap(t, pool, guc, sql); err != nil {
		t.Fatalf("serial re-apply of db/bootstrap.sql after the concurrent race did not converge: %v", err)
	}

	for _, tc := range []struct{ role, password string }{
		{"invoice_migrator", guc.migrator.value},
		{"invoice_app", guc.app.value},
		{"invoice_tenant_reader", guc.reader.value},
	} {
		if err := attemptLogin(t, loginDSN(t, superDSN, tc.role, tc.password)); err != nil {
			t.Errorf("%s: login after the concurrent race + serial re-apply failed — end state did not converge: %v", tc.role, err)
		}
		attrs := readRoleAttrs(t, pool, tc.role)
		if attrs.super || attrs.bypassRLS || attrs.createDB || attrs.createRole || !attrs.canLogin {
			t.Errorf("%s: attributes did not converge cleanly: %+v", tc.role, attrs)
		}
	}
}

// TestMakefileDevDBPipesBootstrapViaExplicitFileFlag: a static (no-database-needed)
// regression guard for the highest-risk fix in this subtask. Confirmed empirically
// during QA: psql silently ignores redirected stdin once ANY `-c` flag is also
// given — it exits 0, emits none of db/bootstrap.sql's command tags (no DO/ALTER
// ROLE/GRANT/REVOKE), and leaves every role's password untouched — unless the input
// is also named explicitly via `-f -`. `make dev-db`'s in-container invocation sets
// three `-c "SELECT set_config(...)"` GUCs and pipes db/bootstrap.sql over stdin, so
// it depends entirely on that explicit `-f -` to not be a silent no-op. This guards
// against a future edit reverting to bare `< db/bootstrap.sql` redirection.
func TestMakefileDevDBPipesBootstrapViaExplicitFileFlag(t *testing.T) {
	b, err := os.ReadFile("../../../Makefile")
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	mk := string(b)

	idx := strings.Index(mk, "docker compose exec -T postgres psql")
	if idx == -1 {
		t.Fatalf("Makefile no longer contains the dev-db target's in-container psql invocation; this test needs updating")
	}
	window := mk[idx:min(idx+800, len(mk))]

	if !strings.Contains(window, "< db/bootstrap.sql") {
		t.Fatalf("dev-db recipe no longer pipes db/bootstrap.sql via stdin redirection; this test needs updating:\n%s", window)
	}
	if !strings.Contains(window, "-f -") {
		t.Errorf("dev-db recipe pipes db/bootstrap.sql via stdin without an explicit `-f -` flag — psql silently ignores redirected stdin once any -c flag is present (confirmed empirically), which would make the whole roles/passwords bootstrap step a silent no-op:\n%s", window)
	}
}

// ---- [M4-21-03] Go provisioning runner (Test-first RED, Mode A) ------------------
//
// Pre-authored (task-127) BEFORE internal/platform/db/bootstrap.go's real bodies
// exist — Test-first: yes. bootstrap.go currently ships only Mode-A stubs (a
// hardcoded false / a "not implemented" error) so this file compiles; every test
// below fails on an ASSERTION against that stub, never on a missing symbol.
//
// DB-backed cases follow the same conventions as the M4-21-02 section above: skip
// on DATABASE_SUPERUSER_URL only, mutate the SAME three shared roles as the rest
// of this file so none use t.Parallel(), and register their restore-to-baseline
// t.Cleanup BEFORE mutating anything, so a RED failure never leaves the shared
// roles rotated for the rest of the package's run. TestBootstrapEnabledAllowlist
// is the one exception: task-127 requires the allowlist guard to be a pure
// unit test with no DB dependency at all, so `go test ./...` stays green with no
// database.

// TestBootstrapEnabledAllowlist: Test Spec row 1 / AC-3. The crux fail-closed
// behavior (QA F1, BLOCKER): db.BootstrapEnabled must be an ALLOWLIST, not a
// blocklist — unset/unknown/production-lookalike environments are false
// regardless of the flag, and only "development" or a PR-env name shape is ever
// true, and only with flag == "true" exactly. The first ten cases are the
// architect's own Test Specs row verbatim; the rest are the "production
// lookalikes" (mixed case, surrounding whitespace) and PR-env-shape edge cases
// this task's brief calls out as required assertions for this specific guard.
func TestBootstrapEnabledAllowlist(t *testing.T) {
	cases := []struct {
		name        string
		environment string
		flag        string
		want        bool
	}{
		// --- architect's Test Specs row, verbatim ---
		{`("development","true")`, "development", "true", true},
		{`("pr-42","true")`, "pr-42", "true", true},
		{`("invoice-os-pr-42","true")`, "invoice-os-pr-42", "true", true},
		{`("production","true")`, "production", "true", false},
		{`("development","false")`, "development", "false", false},
		{`("production","")`, "production", "", false},
		{`("","true") — QA F1: was true under the old blocklist`, "", "true", false},
		{`("staging","true") — unrecognised value, QA F1`, "staging", "true", false},
		{`("Production","true")`, "Production", "true", false},
		{`("prod","true")`, "prod", "true", false},

		// --- additional required assertions: production/allowlist lookalikes ---
		{"leading-whitespace production", " production", "true", false},
		{"trailing-whitespace production", "production ", "true", false},
		{"leading-whitespace development (not an exact match)", " development", "true", false},
		{"trailing-whitespace development (not an exact match)", "development ", "true", false},
		{"all-caps DEVELOPMENT", "DEVELOPMENT", "true", false},

		// --- additional required assertions: PR-env shape edge cases ---
		{"uppercase PR- prefix", "PR-42", "true", false},
		{"pr- with non-numeric suffix", "pr-abc", "true", false},
		{"pr- with no number", "pr-", "true", false},
		{"pr- with trailing garbage after the number", "pr-42x", "true", false},
		{"pr- with trailing whitespace", "pr-42 ", "true", false},

		// --- additional required assertions: the flag itself must be exactly "true" ---
		{"flag uppercase TRUE", "development", "TRUE", false},
		{"flag numeric 1", "development", "1", false},
		{"flag yes", "invoice-os-pr-42", "yes", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := db.BootstrapEnabled(tc.environment, tc.flag); got != tc.want {
				t.Errorf("BootstrapEnabled(%q, %q) = %v, want %v", tc.environment, tc.flag, got, tc.want)
			}
		})
	}
}

// closedPortDSN rewrites superDSN's host:port to one this process just bound and
// immediately released — guaranteed nobody is listening there — while keeping the
// same user/password/dbname/sslmode. Used by
// TestBootstrapRetriesThenFailsOnUnreachableDB so "unreachable" is a real closed
// port, not a guess at one that happens to be free.
func closedPortDSN(t *testing.T, superDSN string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind a port to find one to close: %v", err)
	}
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split host:port: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("close the port before returning it as 'unreachable': %v", err)
	}

	u, err := url.Parse(superDSN)
	if err != nil {
		t.Fatalf("parse superuser dsn: %v", err)
	}
	u.Host = net.JoinHostPort(u.Hostname(), port)
	return u.String()
}

// advisoryKeyGranted reports whether pg_locks currently shows ANY session holding
// the session-level advisory lock for key (the single-bigint-argument
// pg_advisory_lock form, which Postgres records with objsubid = 1, classid = the
// key's high 32 bits and objid = its low 32 bits). db.BootstrapAdvisoryLockKey is
// a small positive constant, so its classid is always 0 here.
func advisoryKeyGranted(t *testing.T, pool *pgxpool.Pool, key int64) bool {
	t.Helper()
	var held bool
	err := pool.QueryRow(context.Background(),
		`SELECT EXISTS (
			SELECT 1 FROM pg_locks
			WHERE locktype = 'advisory' AND granted
			  AND objsubid = 1 AND classid = 0 AND objid = $1
		)`, key,
	).Scan(&held)
	if err != nil {
		t.Fatalf("query pg_locks for advisory key %d: %v", key, err)
	}
	return held
}

// acquireAdvisoryLockRoundTrip proves key is actually free (not merely absent from
// pg_locks) by acquiring and releasing it on ONE held connection — the real
// guarantee AC-5 promises: "a later boot is not deadlocked by a leaked lock".
// Deliberately uses pool.Acquire (one physical connection) rather than two
// separate pool.QueryRow calls, since a pgxpool.Pool method call may land on a
// different physical connection each time, and a session-level advisory lock is
// tied to the connection that took it.
func acquireAdvisoryLockRoundTrip(t *testing.T, pool *pgxpool.Pool, key int64) {
	t.Helper()
	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire connection to test advisory key %d: %v", key, err)
	}
	defer conn.Release()

	var acquired bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&acquired); err != nil {
		t.Fatalf("pg_try_advisory_lock(%d): %v", key, err)
	}
	if !acquired {
		t.Fatalf("pg_try_advisory_lock(%d) = false — another session still holds it after Bootstrap returned; a leaked lock would deadlock every subsequent boot", key)
	}

	var released bool
	if err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock($1)`, key).Scan(&released); err != nil {
		t.Fatalf("pg_advisory_unlock(%d): %v", key, err)
	}
	if !released {
		t.Errorf("pg_advisory_unlock(%d) = false — this connection did not actually hold the lock it just acquired", key)
	}
}

// TestBootstrapFromEmbedded: Test Spec row 2 / AC-1. Runs db.Bootstrap against
// dbsql.FS (the EMBEDDED copy, go:embed'd from db/), never the on-disk file
// readBootstrapSQL(t) uses elsewhere in this file — this is what proves the
// embedded copy is complete, exactly as TestMigrateUpFromEmbedded does for
// migrations. As with the M4-21-02 section's tests, this package's shared dev/CI
// Postgres already has the three roles bootstrapped and owning the migrated
// schema; forcibly dropping them to honor a literal "empty DB" precondition would
// destroy that schema out from under every other test in this package, so — like
// TestBootstrapSQLCreatesRolesWithGivenPasswords — this exercises the
// create-or-converge path with unique per-run passwords and proves them via an
// actual login, which is exactly what a genuinely fresh/empty DB's first boot
// would also exercise.
func TestBootstrapFromEmbedded(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	t.Cleanup(func() { restoreDevDefaultPasswords(t, pool); restoreSafeAttributes(t, pool) })

	pw := db.RolePasswords{
		Migrator: "boot-embed-mig-" + uuid.NewString(),
		App:      "boot-embed-app-" + uuid.NewString(),
		Reader:   "boot-embed-rdr-" + uuid.NewString(),
	}

	if err := db.Bootstrap(context.Background(), superDSN, pw, dbsql.FS); err != nil {
		t.Fatalf("Bootstrap from embedded dbsql.FS: %v", err)
	}

	for _, tc := range []struct{ role, password string }{
		{"invoice_migrator", pw.Migrator},
		{"invoice_app", pw.App},
		{"invoice_tenant_reader", pw.Reader},
	} {
		if err := attemptLogin(t, loginDSN(t, superDSN, tc.role, tc.password)); err != nil {
			t.Errorf("%s: login with the Bootstrap-injected password failed: %v", tc.role, err)
		}
	}
}

// TestBootstrapRejectsEmptyPasswords: Test Spec row 3 / AC-4. A RolePasswords with
// any ONE field empty must be rejected — with an error naming that field — before
// any statement touches the database. Sentinel passwords are planted on all three
// roles beforehand (mirroring TestBootstrapSQLFailsClosedOnMissingPassword) and
// re-checked afterward: if Bootstrap validated only the empty field and still
// applied the other two (valid) passwords it supplied, that would be a partial,
// non-fail-closed rotation — so EVERY role's sentinel, not just the empty one's,
// must still work after the rejected call.
func TestBootstrapRejectsEmptyPasswords(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)

	sentinels := map[string]string{
		"invoice_migrator":      "boot-empty-sentinel-mig",
		"invoice_app":           "boot-empty-sentinel-app",
		"invoice_tenant_reader": "boot-empty-sentinel-rdr",
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
		name      string
		pw        db.RolePasswords
		wantField string
	}{
		{
			name:      "empty migrator",
			pw:        db.RolePasswords{Migrator: "", App: "boot-empty-app-" + uuid.NewString(), Reader: "boot-empty-rdr-" + uuid.NewString()},
			wantField: "migrator",
		},
		{
			name:      "empty app",
			pw:        db.RolePasswords{Migrator: "boot-empty-mig-" + uuid.NewString(), App: "", Reader: "boot-empty-rdr-" + uuid.NewString()},
			wantField: "app",
		},
		{
			name:      "empty reader",
			pw:        db.RolePasswords{Migrator: "boot-empty-mig-" + uuid.NewString(), App: "boot-empty-app-" + uuid.NewString(), Reader: ""},
			wantField: "reader",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := db.Bootstrap(context.Background(), superDSN, tc.pw, dbsql.FS)
			if err == nil {
				t.Fatalf("Bootstrap with %s: got nil error, want a fail-closed error naming the empty field", tc.name)
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.wantField) {
				t.Errorf("error = %q, want it to name the empty field %q", err.Error(), tc.wantField)
			}
			for role, pw := range sentinels {
				if loginErr := attemptLogin(t, loginDSN(t, superDSN, role, pw)); loginErr != nil {
					t.Errorf("%s: its sentinel password no longer works after the rejected call — the database was modified despite the validation failure: %v", role, loginErr)
				}
			}
		})
	}
}

// TestBootstrapConcurrentCallsSerialiseUnderAdvisoryLock: Test Spec row 4 / AC-5.
// The crux advisory-lock behavior (QA F7): TestBootstrapSQLConcurrentInvocationConverges
// above already proved EMPIRICALLY that concurrent bootstrap.sql execution with no
// serialization can raise Postgres's "tuple concurrently updated" (SQLSTATE
// XX000) — the exact failure mode the advisory lock exists to prevent. Here N=4
// goroutines call db.Bootstrap CONCURRENTLY with the SAME passwords; unlike that
// baseline test, every single call must return nil — the lock is what turns "may
// race" into "always serializes" — and the roles must exist exactly once with the
// final password logging in.
func TestBootstrapConcurrentCallsSerialiseUnderAdvisoryLock(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	t.Cleanup(func() { restoreDevDefaultPasswords(t, pool); restoreSafeAttributes(t, pool) })

	pw := db.RolePasswords{
		Migrator: "boot-conc-run-mig-" + uuid.NewString(),
		App:      "boot-conc-run-app-" + uuid.NewString(),
		Reader:   "boot-conc-run-rdr-" + uuid.NewString(),
	}

	const n = 4
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- db.Bootstrap(context.Background(), superDSN, pw, dbsql.FS)
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("concurrent Bootstrap call returned an error — the advisory lock must serialize these so every call succeeds, not just a race survivor: %v", err)
		}
	}

	for _, tc := range []struct{ role, password string }{
		{"invoice_migrator", pw.Migrator},
		{"invoice_app", pw.App},
		{"invoice_tenant_reader", pw.Reader},
	} {
		if err := attemptLogin(t, loginDSN(t, superDSN, tc.role, tc.password)); err != nil {
			t.Errorf("%s: login after the concurrent Bootstrap calls failed — end state did not converge: %v", tc.role, err)
		}
	}
}

// TestBootstrapReleasesAdvisoryLock: Test Spec row 5 / AC-5. After a completed
// Bootstrap, db.BootstrapAdvisoryLockKey must show granted=false in pg_locks (no
// session left holding it), AND a fresh session must actually be able to acquire
// it — the real guarantee AC-5 promises ("a later boot is not deadlocked by a
// leaked lock"), not merely that a catalog view looks empty.
func TestBootstrapReleasesAdvisoryLock(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	t.Cleanup(func() { restoreDevDefaultPasswords(t, pool); restoreSafeAttributes(t, pool) })

	pw := db.RolePasswords{
		Migrator: "boot-lock-mig-" + uuid.NewString(),
		App:      "boot-lock-app-" + uuid.NewString(),
		Reader:   "boot-lock-rdr-" + uuid.NewString(),
	}
	if err := db.Bootstrap(context.Background(), superDSN, pw, dbsql.FS); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	if advisoryKeyGranted(t, pool, db.BootstrapAdvisoryLockKey) {
		t.Errorf("pg_locks still shows advisory key %d granted after Bootstrap returned — the lock was not released", db.BootstrapAdvisoryLockKey)
	}
	acquireAdvisoryLockRoundTrip(t, pool, db.BootstrapAdvisoryLockKey)
}

// TestBootstrapRetriesThenFailsOnUnreachableDB: Test Spec row 6 / AC-6. A DSN
// pointing at a port this test just closed must make Bootstrap return a non-nil
// error within a bounded window — never hang, never panic. Critically, the error
// must actually be CONNECTION-shaped (mentions "connect"), not just "any non-nil
// error": a stub that immediately returns an unrelated "not implemented" error
// would otherwise satisfy "non-nil, no hang" vacuously without ever having
// attempted a connection at all.
func TestBootstrapRetriesThenFailsOnUnreachableDB(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	dsn := closedPortDSN(t, superDSN)
	pw := db.RolePasswords{Migrator: "x", App: "x", Reader: "x"}

	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- db.Bootstrap(context.Background(), dsn, pw, dbsql.FS) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("Bootstrap against a closed port returned nil, want a connect error")
		}
		if elapsed := time.Since(start); elapsed > 30*time.Second {
			t.Errorf("Bootstrap took %s to fail against a closed port — want a bounded connect-retry window", elapsed)
		}
		if !strings.Contains(strings.ToLower(err.Error()), "connect") {
			t.Errorf("error = %q, want it to indicate a connection failure (e.g. contain \"connect\"), proving Bootstrap actually attempted to reach the DB rather than failing for an unrelated reason", err.Error())
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("Bootstrap against a closed port did not return within 30s — the connect retry is not bounded (hangs)")
	}
}

// TestBootstrapThenMigrateSucceedsAsMigrator: Test Spec row 8 / AC-1. End-to-end
// proof of the one-source invariant (Decision [one-bootstrap-file]): Bootstrap
// issues a migrator password, and logging in as invoice_migrator with EXACTLY
// that password and running MigrateUp must succeed with nothing pending — proving
// the Go runner's password never diverges from what the role actually accepts.
func TestBootstrapThenMigrateSucceedsAsMigrator(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	t.Cleanup(func() { restoreDevDefaultPasswords(t, pool); restoreSafeAttributes(t, pool) })

	pw := db.RolePasswords{
		Migrator: "boot-mig-e2e-" + uuid.NewString(),
		App:      "boot-app-e2e-" + uuid.NewString(),
		Reader:   "boot-rdr-e2e-" + uuid.NewString(),
	}
	if err := db.Bootstrap(context.Background(), superDSN, pw, dbsql.FS); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	migratorDSN := loginDSN(t, superDSN, "invoice_migrator", pw.Migrator)
	if err := db.MigrateUp(context.Background(), migratorDSN, migrations.FS); err != nil {
		t.Fatalf("MigrateUp with the Bootstrap-issued migrator password: %v", err)
	}
}

// ---- QA adversarial coverage (post-implementation, Mode B, task-127) ------------
//
// The tests below were NOT part of the architect's pre-authored Test Specs table
// for M4-21-03; they were added during QA verification of task-127 to close gaps
// mutation testing found or to exercise realistic scenarios the 8 authored rows
// didn't cover: rotation over already-provisioned roles (not just a fresh DB),
// advisory-lock contention from an UNRELATED session, an unbounded-looking PR
// number, a genuinely unreachable (not just closed-port) host, and lock release
// on a mid-sequence failure rather than only the happy path.

// TestBootstrapConvergesWhenRolesAlreadyHaveDifferentPasswords: adversarial
// coverage for AC-1/AC-7's "one-source invariant" under a precondition no Test
// Spec row exercises: NOT a fresh/empty DB, but one where all three roles already
// exist with a DIFFERENT password from a prior Bootstrap run (e.g. a redeployed
// gateway rotating its own secrets). db.Bootstrap must converge every role to the
// NEW password and the OLD password must stop working — proving the Go runner
// rotates deterministically, one layer above what
// TestBootstrapSQLRotatesPasswordDeterministically already proves for the raw SQL
// file directly.
func TestBootstrapConvergesWhenRolesAlreadyHaveDifferentPasswords(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)
	t.Cleanup(func() { restoreDevDefaultPasswords(t, pool); restoreSafeAttributes(t, pool) })

	original := db.RolePasswords{
		Migrator: "boot-rot-old-mig-" + uuid.NewString(),
		App:      "boot-rot-old-app-" + uuid.NewString(),
		Reader:   "boot-rot-old-rdr-" + uuid.NewString(),
	}
	if err := db.Bootstrap(context.Background(), superDSN, original, dbsql.FS); err != nil {
		t.Fatalf("first Bootstrap (planting the OLD passwords): %v", err)
	}
	if err := attemptLogin(t, loginDSN(t, superDSN, "invoice_migrator", original.Migrator)); err != nil {
		t.Fatalf("sanity: login with the old migrator password right after planting it: %v", err)
	}

	rotated := db.RolePasswords{
		Migrator: "boot-rot-new-mig-" + uuid.NewString(),
		App:      "boot-rot-new-app-" + uuid.NewString(),
		Reader:   "boot-rot-new-rdr-" + uuid.NewString(),
	}
	if err := db.Bootstrap(context.Background(), superDSN, rotated, dbsql.FS); err != nil {
		t.Fatalf("second Bootstrap (rotating to NEW passwords over already-provisioned roles): %v", err)
	}

	for _, tc := range []struct{ role, password string }{
		{"invoice_migrator", rotated.Migrator},
		{"invoice_app", rotated.App},
		{"invoice_tenant_reader", rotated.Reader},
	} {
		if err := attemptLogin(t, loginDSN(t, superDSN, tc.role, tc.password)); err != nil {
			t.Errorf("%s: login with the NEW password failed after rotation: %v", tc.role, err)
		}
	}
	if err := attemptLogin(t, loginDSN(t, superDSN, "invoice_migrator", original.Migrator)); err == nil {
		t.Errorf("invoice_migrator: login with the OLD password still succeeded after rotation — Bootstrap did not actually converge to the new password")
	}
}

// TestBootstrapRespectsContextDeadlineUnderAdvisoryLockContention: adversarial
// coverage for AC-5. pg_advisory_lock (the single-bigint blocking form) waits
// indefinitely for a busy lock by design — that's what makes two concurrent
// Bootstrap calls serialize instead of racing CREATE ROLE (QA F7). But
// "serializes" must not silently mean "the Go caller can never bound its own
// wait": a completely UNRELATED session (not another Bootstrap call — e.g. an
// operator's stray psql session, or a lock leaked by a crashed process) holding
// BootstrapAdvisoryLockKey must not make Bootstrap hang forever when the CALLER
// supplies a context with a deadline. This confirms Bootstrap actually plumbs ctx
// through to the pg_advisory_lock Exec, rather than e.g. hardcoding
// context.Background() internally for that call (which would silently defeat any
// caller-side timeout).
func TestBootstrapRespectsContextDeadlineUnderAdvisoryLockContention(t *testing.T) {
	superDSN := requireSuperuserDSN(t)

	holder, err := pgx.Connect(context.Background(), superDSN)
	if err != nil {
		t.Fatalf("connect lock-holder session: %v", err)
	}
	// Plain defers, not t.Cleanup: t.Cleanup callbacks run AFTER the test function's
	// own defers have already unwound, so registering the unlock via t.Cleanup would
	// run it against an already-closed connection. LIFO defer order makes this run
	// unlock-then-close, in that order, before the function returns.
	defer holder.Close(context.Background())
	if _, err := holder.Exec(context.Background(), `SELECT pg_advisory_lock($1)`, db.BootstrapAdvisoryLockKey); err != nil {
		t.Fatalf("holder acquire advisory lock: %v", err)
	}
	defer func() {
		if _, err := holder.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, db.BootstrapAdvisoryLockKey); err != nil {
			t.Errorf("release holder's advisory lock: %v", err)
		}
	}()

	pw := db.RolePasswords{
		Migrator: "boot-contend-mig-" + uuid.NewString(),
		App:      "boot-contend-app-" + uuid.NewString(),
		Reader:   "boot-contend-rdr-" + uuid.NewString(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	start := time.Now()
	err = db.Bootstrap(ctx, superDSN, pw, dbsql.FS)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("Bootstrap succeeded while an unrelated session held the advisory lock the whole time — it should have blocked until the ctx deadline instead")
	}
	if elapsed > 8*time.Second {
		t.Fatalf("Bootstrap took %s to return under lock contention with a 3s context deadline — ctx is not being honored while waiting on pg_advisory_lock", elapsed)
	}
}

// TestBootstrapEnabledAllowlistAcceptsArbitrarilyLargePRNumber: adversarial
// coverage for AC-3. prEnvironmentPattern matches "[0-9]+" with no upper bound on
// digit count and BootstrapEnabled never parses the number as an integer, so a
// very large PR number must still be treated as a VALID shape (no overflow/panic
// risk from an int conversion that doesn't exist) rather than silently rejected —
// Railway PR numbers are unbounded over a repo's lifetime.
func TestBootstrapEnabledAllowlistAcceptsArbitrarilyLargePRNumber(t *testing.T) {
	huge := "pr-99999999999999999999999999999999999999"
	if !db.BootstrapEnabled(huge, "true") {
		t.Errorf("BootstrapEnabled(%q, \"true\") = false, want true (arbitrarily large PR numbers must still match the pr-<N> shape)", huge)
	}
}

// TestBootstrapBoundedAgainstBlackHoleHost: adversarial coverage for AC-6.
// TestBootstrapRetriesThenFailsOnUnreachableDB above covers a CLOSED port
// (immediate ECONNREFUSED); this covers the different failure mode of a
// non-routable/black-hole host (203.0.113.1, RFC 5737 TEST-NET-3 — reserved,
// never routed, no RST ever returned), which exercises the dial-level timeout
// path instead of an instant refusal. With a caller-supplied context deadline,
// Bootstrap must still return within a bounded window rather than waiting out an
// OS's own multi-minute TCP retransmission timeout.
func TestBootstrapBoundedAgainstBlackHoleHost(t *testing.T) {
	dsn := "postgres://postgres:x@203.0.113.1:5432/invoice_os?sslmode=disable"
	pw := db.RolePasswords{Migrator: "x", App: "x", Reader: "x"}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	start := time.Now()
	err := db.Bootstrap(ctx, dsn, pw, dbsql.FS)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("Bootstrap against a black-hole host returned nil, want a connect error")
	}
	if elapsed > 12*time.Second {
		t.Errorf("Bootstrap took %s to fail against a black-hole host with a 4s ctx deadline — connect attempts are not bounded by the caller's context", elapsed)
	}
}

// TestBootstrapReleasesAdvisoryLockAfterMidSequenceFailure: adversarial coverage
// for AC-5. TestBootstrapReleasesAdvisoryLock above only covers the HAPPY path;
// this proves release also happens when bootstrap.sql itself fails PARTWAY
// through execution (after the lock is acquired and the three GUCs are set,
// unlike a Go-level validation rejection which never acquires the lock at all).
// Uses an in-memory fs.FS with a deliberately broken bootstrap.sql so the failure
// is deterministic and doesn't depend on mutating the real file.
//
// NOTE (QA finding, see task-127 verdict): this cannot by itself distinguish
// "the explicit pg_advisory_unlock ran" from "the lock merely evaporated because
// Bootstrap's very next deferred statement closes the session-scoped connection
// anyway" — both defers fire on every return path, and a session-level advisory
// lock is released by Postgres the instant its owning backend disconnects,
// regardless of whether pg_advisory_unlock was ever called. So this test (like
// TestBootstrapReleasesAdvisoryLock) confirms the OBSERVABLE guarantee AC-5
// actually promises ("no session holds it after Bootstrap returns, so a later
// boot isn't deadlocked") even on a mid-sequence failure, but does not by itself
// prove the explicit-unlock code path (bootstrap.go's unlock-error-surfacing
// defer) is exercised — see the mutation-testing note in the QA verdict.
func TestBootstrapReleasesAdvisoryLockAfterMidSequenceFailure(t *testing.T) {
	superDSN := requireSuperuserDSN(t)
	pool := bootstrapSuperuserPool(t, superDSN)

	pw := db.RolePasswords{
		Migrator: "boot-midfail-mig-" + uuid.NewString(),
		App:      "boot-midfail-app-" + uuid.NewString(),
		Reader:   "boot-midfail-rdr-" + uuid.NewString(),
	}
	brokenFS := fstest.MapFS{
		"bootstrap.sql": &fstest.MapFile{Data: []byte(
			`DO $$ BEGIN RAISE EXCEPTION 'deliberate mid-sequence failure for QA coverage'; END $$;`,
		)},
	}

	if err := db.Bootstrap(context.Background(), superDSN, pw, brokenFS); err == nil {
		t.Fatalf("Bootstrap with a deliberately broken bootstrap.sql returned nil, want an error")
	}

	if advisoryKeyGranted(t, pool, db.BootstrapAdvisoryLockKey) {
		t.Errorf("pg_locks still shows advisory key %d granted after a MID-SEQUENCE Bootstrap failure — the lock was not released on the error path", db.BootstrapAdvisoryLockKey)
	}
	acquireAdvisoryLockRoundTrip(t, pool, db.BootstrapAdvisoryLockKey)
}
