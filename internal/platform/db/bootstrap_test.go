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
	"sync"
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
