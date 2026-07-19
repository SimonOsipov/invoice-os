// [M4-21-04] Test-first (RED) suite for db.Provision (task-128): wiring
// bootstrap→migrate→seed into the gateway boot path. Pre-authored BEFORE
// provision.go's real body exists (Test-first: yes) — the shipped stub always
// returns a non-nil "not implemented" error, so every test below fails
// immediately on that assertion, never on a missing symbol (Mode A, per this
// package's M4-21-03 precedent in bootstrap_test.go/seed_test.go).
//
// Design:
//   - Pure-logic cases (guard/ordering/short-circuit) never touch a database.
//     SuperuserDSN/MigrationDSN are deliberately-invalid, DISTINGUISHABLE
//     sentinel strings rather than empty strings — an empty DSN falls back to
//     libpq-style environment/socket defaults (confirmed empirically: behavior
//     varies by machine/OS), whereas an invalid keyword/value string fails
//     Postgres-driver DSN *parsing* deterministically and fast, with no network
//     I/O at all, and pgx/goose both echo the invalid input verbatim in the
//     resulting error — so "was this DSN ever touched?" is provable by a simple
//     substring check on Provision's returned error. Confirmed for both pgx.Connect
//     (used by Bootstrap/Seed) and database/sql+goose (used by MigrateUp).
//   - DB-backed cases follow this package's established env-gated-skip
//     convention (requireProvisionDSNs), reuse the shared seedTenants/
//     seedMemberships/bootstrapRoles fixtures and devDefaultGUCs/
//     bootstrapSuperuserPool/mustCount helpers from bootstrap_test.go/
//     seed_test.go/rls_harness_test.go (same package, db_test), and — where they
//     reset the schema to empty — restore the RLS harness's fixtures in
//     t.Cleanup exactly as TestMigrateUpFromEmbedded does (migrate_test.go), so
//     the rest of the package's run stays green.
//   - TestGatewayMainPassesRawEnvironmentToProvisioningGuard is the static
//     counterpart to the crux test: it reads cmd/gateway/main.go's own source
//     text, the same no-database technique
//     TestMakefileDevDBPipesBootstrapViaExplicitFileFlag (above, in
//     bootstrap_test.go) already established for pinning a call site no Go test
//     can invoke directly.
package db_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3"

	dbsql "github.com/SimonOsipov/invoice-os/db"
	db "github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

// superuserPoisonDSN / migrationPoisonDSN are deliberately-invalid Postgres
// connection strings used as canaries in the pure-logic tests below: neither
// parses as a valid DSN, so pgx (Bootstrap/Seed) or goose/database-sql
// (MigrateUp) fail immediately on parsing — no network I/O, no real database
// required — and each failure's error text echoes the offending string
// verbatim (verified empirically against pgx v5.10.0 / goose v3.27.2). Distinct
// markers let a test tell "bootstrap/seed were attempted" apart from "migrate
// was attempted" purely by which marker (if either) appears in the error.
const (
	superuserPoisonDSN = "provision-test-superuser-canary-should-never-be-dialed"
	migrationPoisonDSN = "provision-test-migration-canary-should-always-be-dialed"
)

// requireProvisionDSNs skips the calling test when either DATABASE_SUPERUSER_URL
// or DATABASE_MIGRATION_URL is unset — the two this whole suite's DB-backed
// cases need at minimum (TestSuperuserDSNNotRetainedForRequestPath additionally
// requires DATABASE_URL and self-skips further if that's absent).
func requireProvisionDSNs(t *testing.T) (superDSN, migDSN string) {
	t.Helper()
	superDSN = os.Getenv("DATABASE_SUPERUSER_URL")
	migDSN = os.Getenv("DATABASE_MIGRATION_URL")
	if superDSN == "" || migDSN == "" {
		t.Skip("DATABASE_SUPERUSER_URL and DATABASE_MIGRATION_URL required; skipping provisioning-sequence integration test")
	}
	return superDSN, migDSN
}

// devRolePasswords adapts this file's devDefaultGUCs() (bootstrap_test.go) —
// which returns the bootstrapGUCs shape Bootstrap-via-SQL tests use — into the
// db.RolePasswords shape db.Provision/db.Bootstrap take directly.
func devRolePasswords() db.RolePasswords {
	g := devDefaultGUCs()
	return db.RolePasswords{Migrator: g.migrator.value, App: g.app.value, Reader: g.reader.value}
}

// ---- Pure-logic: guard, ordering, short-circuit (no database) --------------

// TestProvisionGuardReadsRawEnvironment: Test Spec row / AC-2 — THE CRUX test
// for task-128 (QA F1's second half, "the single most important test in this
// subtask"). ENVIRONMENT is unset in the process env and read raw, mirroring
// main.go's call site exactly; GATEWAY_DB_BOOTSTRAP=true. If Provision's guard
// were defeated (e.g. by a call site that substitutes
// app.Config.Environment's "development" default for an unset ENVIRONMENT
// instead of the raw value), Bootstrap would be attempted against
// superuserPoisonDSN and its marker would appear in the returned error — this
// test fails loudly in that case. The paired positive assertion (the migration
// marker MUST appear) proves the always-on migrate step is still reached, so a
// vacuous "no marker anywhere" is never mistaken for a pass.
func TestProvisionGuardReadsRawEnvironment(t *testing.T) {
	os.Unsetenv("ENVIRONMENT")
	environment := os.Getenv("ENVIRONMENT") // mirrors main.go's raw os.Getenv read verbatim
	if environment != "" {
		t.Fatalf("test setup: ENVIRONMENT = %q after Unsetenv, want empty", environment)
	}

	cfg := db.ProvisionConfig{
		Environment:   environment,
		BootstrapFlag: "true",
		SuperuserDSN:  superuserPoisonDSN,
		MigrationDSN:  migrationPoisonDSN,
		Passwords:     devRolePasswords(), // valid-shaped; irrelevant if the guard correctly skips Bootstrap
		BootstrapFS:   dbsql.FS,
		MigrationsFS:  migrations.FS,
		SeedFS:        dbsql.FS,
	}
	err := db.Provision(context.Background(), cfg)
	if err == nil {
		t.Fatal("Provision returned nil — want an error from the guard-independent migrate step failing against a poison DSN")
	}
	if strings.Contains(err.Error(), superuserPoisonDSN) {
		t.Fatalf("Provision's error mentions the superuser DSN marker — bootstrap/seed were attempted even though ENVIRONMENT was unset (the raw-environment guard was defeated, e.g. by substituting app.Config.Environment's \"development\" default): %v", err)
	}
	if !strings.Contains(err.Error(), migrationPoisonDSN) {
		t.Fatalf("Provision's error does not mention the migration DSN marker — want proof migrate was still attempted (guard-independent, unchanged gateway-as-migrator behavior) and failed against the poison migration DSN: %v", err)
	}
}

// TestGatewayMainPassesRawEnvironmentToProvisioningGuard is the static
// counterpart to TestProvisionGuardReadsRawEnvironment. That test proves
// Provision behaves correctly WHEN GIVEN the raw environment value; it cannot
// prove main.go's call site actually SUPPLIES that raw value rather than
// app.Config.Environment — no test in this package can invoke func main()
// directly. This closes that gap the same way
// TestMakefileDevDBPipesBootstrapViaExplicitFileFlag (above, in
// bootstrap_test.go) pins the Makefile: a no-database source-text assertion
// against cmd/gateway/main.go itself.
func TestGatewayMainPassesRawEnvironmentToProvisioningGuard(t *testing.T) {
	b, err := os.ReadFile("../../../cmd/gateway/main.go")
	if err != nil {
		t.Fatalf("read cmd/gateway/main.go: %v", err)
	}
	src := string(b)

	idx := strings.Index(src, "db.Provision(")
	if idx == -1 {
		idx = strings.Index(src, "BootstrapEnabled(")
	}
	if idx == -1 {
		t.Fatal("cmd/gateway/main.go does not yet call db.Provision(...) or db.BootstrapEnabled(...) — task-128's boot-sequence wiring is not in place yet")
	}

	start := max(0, idx-300)
	end := min(len(src), idx+500)
	window := src[start:end]

	if !strings.Contains(window, `os.Getenv("ENVIRONMENT")`) {
		t.Errorf("cmd/gateway/main.go's provisioning-guard call site does not read the raw os.Getenv(\"ENVIRONMENT\") near the call — window:\n%s", window)
	}
	if strings.Contains(window, "app.Config.Environment") {
		t.Errorf("cmd/gateway/main.go's provisioning-guard call site reads app.Config.Environment (QA F1) — internal/platform/config.go:44 substitutes \"development\" for an unset ENVIRONMENT, silently re-opening the fail-open hole the allowlist guard exists to close. Window:\n%s", window)
	}
	if !strings.Contains(window, `os.Getenv("GATEWAY_DB_BOOTSTRAP")`) {
		t.Errorf("cmd/gateway/main.go's provisioning-guard call site does not read os.Getenv(\"GATEWAY_DB_BOOTSTRAP\") near the call — window:\n%s", window)
	}
}

// TestProvisionSkippedWhenGuardOff: Test Spec row / AC-3. Same poison-DSN
// technique as the crux test above, for the OTHER allowlist-false input the
// task's brief calls out explicitly: ENVIRONMENT=production (rather than
// unset), superuser DSN not required.
func TestProvisionSkippedWhenGuardOff(t *testing.T) {
	cfg := db.ProvisionConfig{
		Environment:   "production",
		BootstrapFlag: "true",
		SuperuserDSN:  superuserPoisonDSN,
		MigrationDSN:  migrationPoisonDSN,
		Passwords:     db.RolePasswords{}, // guard off: must never even be looked at
		BootstrapFS:   dbsql.FS,
		MigrationsFS:  migrations.FS,
		SeedFS:        dbsql.FS,
	}
	err := db.Provision(context.Background(), cfg)
	if err == nil {
		t.Fatal("Provision returned nil — want an error from the guard-independent migrate step failing against a poison DSN")
	}
	if strings.Contains(err.Error(), superuserPoisonDSN) {
		t.Fatalf("Provision's error mentions the superuser DSN marker — bootstrap/seed were attempted with ENVIRONMENT=production: %v", err)
	}
	if !strings.Contains(err.Error(), migrationPoisonDSN) {
		t.Fatalf("Provision's error does not mention the migration DSN marker — want proof migrate was still attempted: %v", err)
	}
}

// TestProvisionMissingPasswordFailsLoudly: "ALSO TEST" item 5 in task-128's
// brief. Guard ON, but RolePasswords.Migrator is empty — Bootstrap validates
// RolePasswords BEFORE opening any connection (bootstrap.go's
// validateRolePasswords), so this is pure logic: no network I/O either way.
// Provisioning enabled + a required password absent must fail loudly naming the
// missing field, never silently skip provisioning.
func TestProvisionMissingPasswordFailsLoudly(t *testing.T) {
	cfg := db.ProvisionConfig{
		Environment:   "development",
		BootstrapFlag: "true",
		SuperuserDSN:  superuserPoisonDSN,
		MigrationDSN:  migrationPoisonDSN,
		Passwords:     db.RolePasswords{Migrator: "", App: "app-pw", Reader: "reader-pw"},
		BootstrapFS:   dbsql.FS,
		MigrationsFS:  migrations.FS,
		SeedFS:        dbsql.FS,
	}
	err := db.Provision(context.Background(), cfg)
	if err == nil {
		t.Fatal("Provision succeeded with an empty RolePasswords.Migrator — want a loud failure naming the missing field")
	}
	if !strings.Contains(err.Error(), "Migrator") {
		t.Errorf("Provision error = %q, want it to name the missing RolePasswords field (\"Migrator\")", err.Error())
	}
	if strings.Contains(err.Error(), migrationPoisonDSN) {
		t.Errorf("Provision error = %q, mentions the migration DSN marker — password validation must fail BEFORE migrate is ever attempted", err.Error())
	}
}

// TestProvisionBootstrapFailureShortCircuitsMigrateAndSeed: "ALSO TEST" item 1
// in task-128's brief (ordering + short-circuit). Guard ON with an entirely
// empty RolePasswords makes Bootstrap fail deterministically at its own
// pre-connection validation (bootstrap.go's validateRolePasswords) — pure
// logic, no network I/O. If bootstrap fails, migrate must never run: proven by
// asserting the migration DSN marker never appears in Provision's error (if
// migrate had been reached despite the earlier failure, its poison DSN would
// surface a second, distinguishable error).
func TestProvisionBootstrapFailureShortCircuitsMigrateAndSeed(t *testing.T) {
	cfg := db.ProvisionConfig{
		Environment:   "development",
		BootstrapFlag: "true",
		SuperuserDSN:  superuserPoisonDSN,
		MigrationDSN:  migrationPoisonDSN,
		Passwords:     db.RolePasswords{}, // all empty: Bootstrap must fail at validation, before any connection
		BootstrapFS:   dbsql.FS,
		MigrationsFS:  migrations.FS,
		SeedFS:        dbsql.FS,
	}
	err := db.Provision(context.Background(), cfg)
	if err == nil {
		t.Fatal("Provision succeeded despite empty RolePasswords — want the bootstrap validation failure, and nothing past it")
	}
	if !strings.Contains(err.Error(), "RolePasswords") {
		t.Errorf("Provision error = %q, want Bootstrap's own RolePasswords validation error specifically", err.Error())
	}
	if strings.Contains(err.Error(), migrationPoisonDSN) {
		t.Errorf("Provision error = %q, mentions the migration DSN marker — migrate must not run after bootstrap fails (boot order is bootstrap → migrate → seed, short-circuiting on the first error)", err.Error())
	}
}

// ---- DB-backed: the full sequence against a real Postgres -------------------

// TestProvisionFromEmptyDatabase: Test Spec row / AC-1. A Postgres reset to an
// empty schema (roles are NOT dropped — see bootstrap_test.go's header comment
// on why; Bootstrap's create-or-converge path, exercised here, is exactly what
// runs the first time against a genuinely fresh DB) → run Provision → roles
// exist, every migration applied with nothing pending, and the 4 seed tenants
// are present.
func TestProvisionFromEmptyDatabase(t *testing.T) {
	superDSN, migDSN := requireProvisionDSNs(t)
	ctx := context.Background()

	sqlDB, err := sql.Open("pgx", migDSN)
	if err != nil {
		t.Fatalf("open migrator connection: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		t.Fatalf("build migration provider: %v", err)
	}

	// DownTo(0) below wipes the tenants the RLS harness seeds in TestMain;
	// restore its fixtures afterward so the rest of the package's run stays
	// green — mirrors TestMigrateUpFromEmbedded's precedent exactly
	// (migrate_test.go).
	if h != nil {
		t.Cleanup(func() {
			cctx := context.Background()
			if err := db.MigrateUp(cctx, migDSN, migrations.FS); err != nil {
				t.Errorf("restore schema after provision-from-empty round-trip: %v", err)
				return
			}
			if err := h.restore(cctx); err != nil {
				t.Errorf("restore RLS harness fixtures: %v", err)
			}
		})
	}

	if _, err := provider.DownTo(ctx, 0); err != nil {
		t.Fatalf("reset to empty (down to 0): %v", err)
	}

	cfg := db.ProvisionConfig{
		Environment:   "development",
		BootstrapFlag: "true",
		SuperuserDSN:  superDSN,
		MigrationDSN:  migDSN,
		Passwords:     devRolePasswords(),
		BootstrapFS:   dbsql.FS,
		MigrationsFS:  migrations.FS,
		SeedFS:        dbsql.FS,
	}
	if err := db.Provision(ctx, cfg); err != nil {
		t.Fatalf("Provision from an empty schema: %v", err)
	}

	pool := bootstrapSuperuserPool(t, superDSN)

	for _, role := range bootstrapRoles {
		count := mustCount(t, pool, `SELECT count(*) FROM pg_roles WHERE rolname = $1`, role)
		if count != 1 {
			t.Errorf("role %s: found %d rows in pg_roles after Provision, want exactly 1", role, count)
		}
	}

	version, err := provider.GetDBVersion(ctx)
	if err != nil {
		t.Fatalf("get db version: %v", err)
	}
	if version == 0 {
		t.Fatalf("db version = 0 after Provision, want the schema fully migrated")
	}
	again, err := provider.Up(ctx)
	if err != nil {
		t.Fatalf("second Up after Provision: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second Up applied %d migration(s) after Provision, want 0 pending", len(again))
	}

	for _, tc := range seedTenants {
		var count int
		var kind string
		err := pool.QueryRow(ctx,
			`SELECT count(*), max(kind) FROM tenants WHERE id = $1 AND name = $2 GROUP BY kind`,
			tc.id, tc.name,
		).Scan(&count, &kind)
		if err != nil {
			t.Fatalf("query tenant %s (%s): %v (does it exist at all after Provision?)", tc.id, tc.name, err)
		}
		if count != 1 {
			t.Errorf("tenant %s (%s): found %d rows after Provision, want exactly 1", tc.id, tc.name, count)
		}
		if kind != tc.kind {
			t.Errorf("tenant %s (%s): kind = %q, want %q", tc.id, tc.name, kind, tc.kind)
		}
	}

	// [M4-22-03 QA] The central claim task-162 exists to satisfy: a freshly
	// spawned PR environment (an empty schema, exactly what this test starts
	// from) is demo-ready at boot with NO post-boot command -- the curated
	// business_entities portfolio and the all-rules-enabled state must hold
	// after Provision, exercised through the REAL boot entry point (Provision,
	// which drives Bootstrap -> MigrateUp -> Seed) rather than calling db.Seed
	// directly the way seed_demo_test.go's suite does. Reuses
	// fetchDemoBusinessEntities/demoTenantID (seed_demo_test.go /
	// demo_reset_test.go, same package) rather than duplicating the query.
	assertDemoReady := func(step string) {
		t.Helper()
		entities := fetchDemoBusinessEntities(t, pool, demoTenantID)
		if len(entities) != 27 {
			t.Fatalf("%s: count(business_entities) for the demo tenant = %d, want 27", step, len(entities))
		}
		var active, archived int
		for _, r := range entities {
			switch r.status {
			case "active":
				active++
			case "archived":
				archived++
			}
		}
		if active != 21 {
			t.Errorf("%s: count(active business_entities) = %d, want 21", step, active)
		}
		if archived != 6 {
			t.Errorf("%s: count(archived business_entities) = %d, want 6", step, archived)
		}
		if disabled := mustCount(t, pool, `SELECT count(*) FROM rules WHERE enabled = false`); disabled != 0 {
			t.Errorf("%s: count(rules WHERE enabled=false) = %d, want 0", step, disabled)
		}
	}
	assertDemoReady("after the FIRST Provision (from an empty schema)")

	// Re-run Provision a second time against the now-provisioned schema
	// (simulating a redeploy or a second replica booting against the same
	// freshly-provisioned env) and assert the demo-ready state still holds,
	// unduplicated and unregressed -- not just that db.Seed alone is
	// idempotent (TestSeedDemoEntitiesIsIdempotent/TestSeedReenablesDisabledRules),
	// but that the FULL Provision sequence run twice from a cold boot lands in
	// the same demo-ready state both times.
	if err := db.Provision(ctx, cfg); err != nil {
		t.Fatalf("second Provision (idempotency, from the now-provisioned schema): %v", err)
	}
	assertDemoReady("after the SECOND Provision")
}

// TestProvisionSeedFailsIfRunBeforeMigrate: Test Spec row / AC-1. Bootstrapped
// but unmigrated DB → call Seed → an error naming the missing relation, pinning
// that seed must follow migrate.
//
// NOT RED under Mode A (flagged per this task's brief, "any Test Spec row not
// expressible"): this exercises db.Bootstrap and db.Seed directly, both
// already shipped (real, non-stub) by M4-21-03 — no code this subtask adds is
// on the call path, so this test already passes today against real Postgres
// behavior (confirmed empirically: Seed on an unmigrated schema fails with
// `ERROR: relation "tenants" does not exist (SQLSTATE 42P01)`). It is included
// anyway as a regression-pinning test documenting AC-1's ordering rationale
// (migrate before seed) and stays green through Mode B.
func TestProvisionSeedFailsIfRunBeforeMigrate(t *testing.T) {
	superDSN, migDSN := requireProvisionDSNs(t)
	ctx := context.Background()

	sqlDB, err := sql.Open("pgx", migDSN)
	if err != nil {
		t.Fatalf("open migrator connection: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		t.Fatalf("build migration provider: %v", err)
	}

	if h != nil {
		t.Cleanup(func() {
			cctx := context.Background()
			if err := db.MigrateUp(cctx, migDSN, migrations.FS); err != nil {
				t.Errorf("restore schema after unmigrated-seed test: %v", err)
				return
			}
			if err := h.restore(cctx); err != nil {
				t.Errorf("restore RLS harness fixtures: %v", err)
			}
		})
	}

	if _, err := provider.DownTo(ctx, 0); err != nil {
		t.Fatalf("reset to empty (down to 0): %v", err)
	}

	// Bootstrap first (roles must exist for Seed to even authenticate) — the
	// real db.Bootstrap, already shipped by M4-21-03; this test's subject is
	// Seed's behavior on an unmigrated schema, not Bootstrap's.
	if err := db.Bootstrap(ctx, superDSN, devRolePasswords(), dbsql.FS); err != nil {
		t.Fatalf("Bootstrap (precondition): %v", err)
	}

	err = db.Seed(ctx, superDSN, dbsql.FS)
	if err == nil {
		t.Fatal("Seed succeeded against an unmigrated schema — want an error naming the missing relation (tenants/memberships), pinning that seed must run after migrate")
	}
	lower := strings.ToLower(err.Error())
	if !strings.Contains(lower, "relation") {
		t.Errorf("Seed error = %q, want it to mention the missing relation (Postgres's \"relation ... does not exist\")", err.Error())
	}
}

// TestSuperuserDSNNotRetainedForRequestPath: Test Spec row / AC-4 (QA F3).
// Requires DATABASE_URL in addition to the superuser/migration DSNs
// requireProvisionDSNs already gates on — self-skips further if absent (the CI
// `migrations` job does not currently export DATABASE_URL; wiring that is a
// follow-up for whichever job/step ends up running this test, tracked
// separately from this Mode-A test-authoring pass).
//
// Runs the full sequence with a superuser DSN distinct from DATABASE_URL, then
// asserts (i) the provisioning connection is closed — via a pg_stat_activity
// connection-count delta for the superuser role, taken before and after
// Provision, using the SAME monitoring pool both times so the monitor's own
// connection(s) cancel out of the delta — and (ii) the pool the app serves
// requests from was constructed from DATABASE_URL and reports
// current_user = invoice_app / rolbypassrls = false — never the superuser.
func TestSuperuserDSNNotRetainedForRequestPath(t *testing.T) {
	superDSN, migDSN := requireProvisionDSNs(t)
	appDSN := os.Getenv("DATABASE_URL")
	if appDSN == "" {
		t.Skip("DATABASE_URL not set; skipping request-path pool assertion")
	}
	ctx := context.Background()

	monitor := bootstrapSuperuserPool(t, superDSN)
	superCfg, err := pgx.ParseConfig(superDSN)
	if err != nil {
		t.Fatalf("parse superuser DSN: %v", err)
	}
	before := mustCount(t, monitor, `SELECT count(*) FROM pg_stat_activity WHERE usename = $1`, superCfg.User)

	cfg := db.ProvisionConfig{
		Environment:   "development",
		BootstrapFlag: "true",
		SuperuserDSN:  superDSN,
		MigrationDSN:  migDSN,
		Passwords:     devRolePasswords(),
		BootstrapFS:   dbsql.FS,
		MigrationsFS:  migrations.FS,
		SeedFS:        dbsql.FS,
	}
	if err := db.Provision(ctx, cfg); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	after := mustCount(t, monitor, `SELECT count(*) FROM pg_stat_activity WHERE usename = $1`, superCfg.User)
	if after != before {
		t.Errorf("pg_stat_activity shows %d %q connection(s) after Provision returned (had %d before) — the provisioning connection was not closed / was retained past Provision's return", after, superCfg.User, before)
	}

	appPool, err := db.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatalf("open request-path pool from DATABASE_URL: %v", err)
	}
	t.Cleanup(appPool.Close)

	var user string
	var bypass bool
	if err := appPool.QueryRow(ctx, `SELECT current_user, rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&user, &bypass); err != nil {
		t.Fatalf("query request-path pool identity: %v", err)
	}
	if user != "invoice_app" {
		t.Errorf("request-path pool current_user = %q, want %q — the superuser DSN must never construct the request-serving pool", user, "invoice_app")
	}
	if bypass {
		t.Errorf("request-path pool rolbypassrls = true, want false — invoice_app must be NOBYPASSRLS for RLS to be enforceable")
	}
}

// TestProvisionIsIdempotentAcrossRedeploys: Test Spec row / AC-7. A fully
// provisioned DB (the shared dev/CI Postgres every other test in this package
// already depends on) → run the whole sequence twice → no error either time,
// no duplicate tenants/memberships, no pending migrations. Pins that a redeploy
// of the persistent `development` environment (Decision [dev-env-status]) never
// wipes or corrupts existing data.
func TestProvisionIsIdempotentAcrossRedeploys(t *testing.T) {
	superDSN, migDSN := requireProvisionDSNs(t)
	ctx := context.Background()
	pool := bootstrapSuperuserPool(t, superDSN)

	cfg := db.ProvisionConfig{
		Environment:   "development",
		BootstrapFlag: "true",
		SuperuserDSN:  superDSN,
		MigrationDSN:  migDSN,
		Passwords:     devRolePasswords(),
		BootstrapFS:   dbsql.FS,
		MigrationsFS:  migrations.FS,
		SeedFS:        dbsql.FS,
	}

	if err := db.Provision(ctx, cfg); err != nil {
		t.Fatalf("first Provision (against an already-provisioned DB): %v", err)
	}
	if err := db.Provision(ctx, cfg); err != nil {
		t.Fatalf("second Provision (idempotency): %v", err)
	}

	for _, tc := range seedTenants {
		count := mustCount(t, pool, `SELECT count(*) FROM tenants WHERE id = $1 AND name = $2`, tc.id, tc.name)
		if count != 1 {
			t.Errorf("tenant %s (%s): found %d rows after two Provision calls, want exactly 1 (redeploy must not duplicate seed rows)", tc.id, tc.name, count)
		}
	}
	for _, tc := range seedMemberships {
		count := mustCount(t, pool, `SELECT count(*) FROM memberships WHERE tenant_id = $1 AND user_id = $2`, tc.tenantID, tc.userID)
		if count != 1 {
			t.Errorf("membership %s/%s: found %d rows after two Provision calls, want exactly 1", tc.tenantID, tc.userID, count)
		}
	}

	sqlDB, err := sql.Open("pgx", migDSN)
	if err != nil {
		t.Fatalf("open migrator connection: %v", err)
	}
	defer func() { _ = sqlDB.Close() }()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		t.Fatalf("build migration provider: %v", err)
	}
	pending, err := provider.Up(ctx)
	if err != nil {
		t.Fatalf("Up after two Provision calls: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("Up after two Provision calls applied %d migration(s), want 0 pending", len(pending))
	}
}

// TestProvisionGuardOffStillMigratesAgainstRealDB: reinforces AC-3's positive
// path with a real, reachable migration DSN (the pure-logic
// TestProvisionSkippedWhenGuardOff above only proves migrate is ATTEMPTED, via
// its poison DSN's error marker — this proves it actually SUCCEEDS). Guard off
// (ENVIRONMENT=production) with a poison superuser DSN and empty passwords:
// if Bootstrap/Seed were mistakenly attempted, this would fail against the
// poison DSN; Provision must instead return nil, having only migrated.
func TestProvisionGuardOffStillMigratesAgainstRealDB(t *testing.T) {
	_, migDSN := requireProvisionDSNs(t)
	ctx := context.Background()

	cfg := db.ProvisionConfig{
		Environment:   "production",
		BootstrapFlag: "true",
		SuperuserDSN:  superuserPoisonDSN, // guard is off: must never be dialed
		MigrationDSN:  migDSN,             // real, reachable: migrate must still run and succeed
		Passwords:     db.RolePasswords{}, // guard is off: Bootstrap/Seed must never even look at these
		BootstrapFS:   dbsql.FS,
		MigrationsFS:  migrations.FS,
		SeedFS:        dbsql.FS,
	}
	if err := db.Provision(ctx, cfg); err != nil {
		t.Fatalf("Provision with ENVIRONMENT=production against a real migration DB: %v (guard-off must still migrate cleanly; Bootstrap/Seed must be skipped, not attempted against the poison superuser DSN)", err)
	}
}

// ---- QA adversarial coverage (task-128 verification pass) -------------------
//
// The tests below were added during QA verification of task-128, on top of the
// architect's pre-authored Test Specs above. They target failure modes the
// brief called out explicitly: an unreachable (not just unparseable) superuser
// DSN, the OTHER allowlisted shape (a Railway PR environment) exercised through
// the full sequence rather than BootstrapEnabled alone, a mid-sequence failure
// (bootstrap succeeds, migrate fails) recovering cleanly on the next boot, two
// replicas racing Provision concurrently against one DB, and the data-loss risk
// specific to the PERSISTENT `development` environment (Decision
// [dev-env-status]): a redeploy must never destroy data that isn't part of
// seed.dev.sql.

// TestProvisionUnreachableSuperuserDSNFailsBounded: guard on, a syntactically
// valid superuser DSN pointing at a port nothing listens on (loopback,
// connection REFUSED — not a slow black-hole address, so this test's runtime
// is deterministic across machines/CI with no context timeout of its own
// needed). Provision must fail within the bounded connect-retry budget
// (bootstrapConnectAttempts * bootstrapConnectBackoff ~= 2s), never hang, and
// migrate must never be reached (bootstrap fails first).
func TestProvisionUnreachableSuperuserDSNFailsBounded(t *testing.T) {
	cfg := db.ProvisionConfig{
		Environment:   "development",
		BootstrapFlag: "true",
		SuperuserDSN:  "postgres://postgres:postgres@127.0.0.1:1/invoice_os?sslmode=disable",
		MigrationDSN:  migrationPoisonDSN, // never reached: bootstrap's connect fails first
		Passwords:     devRolePasswords(),
		BootstrapFS:   dbsql.FS,
		MigrationsFS:  migrations.FS,
		SeedFS:        dbsql.FS,
	}

	start := time.Now()
	err := db.Provision(context.Background(), cfg)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Provision succeeded against an unreachable (connection-refused) superuser DSN — want a bounded connection error")
	}
	if elapsed > 10*time.Second {
		t.Fatalf("Provision took %s against an unreachable superuser DSN — want a bounded failure (retry budget ~2s), not an unbounded hang", elapsed)
	}
	if strings.Contains(err.Error(), migrationPoisonDSN) {
		t.Errorf("Provision error = %q, mentions the migration DSN marker — migrate must not run after bootstrap's connect failure: elapsed=%s", err.Error(), elapsed)
	}
}

// TestProvisionPRShapedEnvironmentEndToEnd: AC-2's OTHER allowlisted shape
// (BootstrapEnabled's provisionableEnvironment also accepts a Railway
// PR-environment name, e.g. "pr-42") exercised through the FULL boot sequence
// against a real Postgres — not just the pure BootstrapEnabled unit check
// elsewhere in this package. Runs against the shared dev/CI DB every other
// test in this package depends on, so it also doubles as an idempotency check
// (no duplicate roles/tenants after yet another Provision call).
func TestProvisionPRShapedEnvironmentEndToEnd(t *testing.T) {
	superDSN, migDSN := requireProvisionDSNs(t)
	ctx := context.Background()
	pool := bootstrapSuperuserPool(t, superDSN)

	cfg := db.ProvisionConfig{
		Environment:   "pr-42",
		BootstrapFlag: "true",
		SuperuserDSN:  superDSN,
		MigrationDSN:  migDSN,
		Passwords:     devRolePasswords(),
		BootstrapFS:   dbsql.FS,
		MigrationsFS:  migrations.FS,
		SeedFS:        dbsql.FS,
	}
	if err := db.Provision(ctx, cfg); err != nil {
		t.Fatalf("Provision with ENVIRONMENT=pr-42 (a Railway PR-environment name): %v", err)
	}

	for _, role := range bootstrapRoles {
		count := mustCount(t, pool, `SELECT count(*) FROM pg_roles WHERE rolname = $1`, role)
		if count != 1 {
			t.Errorf("role %s: found %d rows in pg_roles after a pr-42 Provision, want exactly 1", role, count)
		}
	}
	for _, tc := range seedTenants {
		count := mustCount(t, pool, `SELECT count(*) FROM tenants WHERE id = $1 AND name = $2`, tc.id, tc.name)
		if count != 1 {
			t.Errorf("tenant %s (%s): found %d rows after a pr-42 Provision, want exactly 1 (no duplicate seed rows)", tc.id, tc.name, count)
		}
	}
}

// TestProvisionMigrateFailureAfterBootstrapSuccessRecoversCleanly: the
// "partial provisioning" adversarial case — bootstrap succeeds (real superuser
// DSN + passwords) but migrate fails (poison migration DSN). Provision must
// fail loudly naming the migrate step, AND a follow-up boot with a real
// migration DSN must succeed cleanly: bootstrap.sql's create-or-converge
// idempotency means the earlier partial success (roles created/reasserted)
// left no half-state that blocks the next deploy from a Railway restart.
func TestProvisionMigrateFailureAfterBootstrapSuccessRecoversCleanly(t *testing.T) {
	superDSN, migDSN := requireProvisionDSNs(t)
	ctx := context.Background()

	badCfg := db.ProvisionConfig{
		Environment:   "development",
		BootstrapFlag: "true",
		SuperuserDSN:  superDSN,           // real: bootstrap must succeed
		MigrationDSN:  migrationPoisonDSN, // poison: migrate fails AFTER bootstrap ran
		Passwords:     devRolePasswords(),
		BootstrapFS:   dbsql.FS,
		MigrationsFS:  migrations.FS,
		SeedFS:        dbsql.FS,
	}
	err := db.Provision(ctx, badCfg)
	if err == nil {
		t.Fatal("Provision succeeded despite a poison migration DSN — want a loud error naming the migrate step")
	}
	if !strings.Contains(err.Error(), "migrate") {
		t.Errorf("Provision error = %q, want it to name the migrate step", err.Error())
	}
	if !strings.Contains(err.Error(), migrationPoisonDSN) {
		t.Errorf("Provision error = %q, want it to echo the poison migration DSN (proving migrate was actually attempted after bootstrap succeeded, not short-circuited earlier)", err.Error())
	}

	// Recovery: a follow-up boot with a real migration DSN must succeed cleanly.
	goodCfg := badCfg
	goodCfg.MigrationDSN = migDSN
	if err := db.Provision(ctx, goodCfg); err != nil {
		t.Fatalf("recovery Provision (real migration DSN) after the earlier partial failure: %v — the partial failure left the DB in a state that blocks the next deploy", err)
	}

	pool := bootstrapSuperuserPool(t, superDSN)
	for _, role := range bootstrapRoles {
		count := mustCount(t, pool, `SELECT count(*) FROM pg_roles WHERE rolname = $1`, role)
		if count != 1 {
			t.Errorf("role %s: found %d rows in pg_roles after recovery, want exactly 1", role, count)
		}
	}
}

// TestProvisionConcurrentBootsSerializeCleanly: simulates two-or-more gateway
// replicas racing Provision against the SAME database at boot — the realistic
// Railway multi-replica scenario, not just db.Bootstrap in isolation (already
// covered by TestBootstrapConcurrentCallsSerialiseUnderAdvisoryLock in
// bootstrap_test.go). Every concurrent Provision call must succeed (the
// advisory lock serializes bootstrap; migrate/seed are naturally idempotent),
// and the seed rows must still appear exactly once afterward — no duplication,
// no corruption from the race.
func TestProvisionConcurrentBootsSerializeCleanly(t *testing.T) {
	superDSN, migDSN := requireProvisionDSNs(t)
	ctx := context.Background()

	const n = 4
	cfg := db.ProvisionConfig{
		Environment:   "development",
		BootstrapFlag: "true",
		SuperuserDSN:  superDSN,
		MigrationDSN:  migDSN,
		Passwords:     devRolePasswords(),
		BootstrapFS:   dbsql.FS,
		MigrationsFS:  migrations.FS,
		SeedFS:        dbsql.FS,
	}

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = db.Provision(ctx, cfg)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent Provision call %d failed — the advisory lock must serialize concurrent boots so every replica converges, not just one survivor: %v", i, err)
		}
	}

	pool := bootstrapSuperuserPool(t, superDSN)
	for _, tc := range seedTenants {
		count := mustCount(t, pool, `SELECT count(*) FROM tenants WHERE id = $1 AND name = $2`, tc.id, tc.name)
		if count != 1 {
			t.Errorf("tenant %s (%s): found %d rows after %d concurrent Provision calls, want exactly 1 (no duplicate/corrupted seed rows)", tc.id, tc.name, count, n)
		}
	}
	for _, tc := range seedMemberships {
		count := mustCount(t, pool, `SELECT count(*) FROM memberships WHERE tenant_id = $1 AND user_id = $2`, tc.tenantID, tc.userID)
		if count != 1 {
			t.Errorf("membership %s/%s: found %d rows after %d concurrent Provision calls, want exactly 1", tc.tenantID, tc.userID, count, n)
		}
	}
}

// TestProvisionRedeployDoesNotDestroyPreexistingData: the data-loss risk
// specific to the PERSISTENT `development` environment (Decision
// [dev-env-status]) — a tenant NOT in seed.dev.sql (simulating real
// production/demo data accumulated between deploys) must survive a Provision
// call untouched. A naive/destructive implementation (e.g. one that
// reset-and-reseeded instead of idempotently upserting fixed rows) would wipe
// it; this proves data safety directly rather than trusting "seed is
// upsert-only" by inspection.
func TestProvisionRedeployDoesNotDestroyPreexistingData(t *testing.T) {
	superDSN, migDSN := requireProvisionDSNs(t)
	ctx := context.Background()
	pool := bootstrapSuperuserPool(t, superDSN)

	const probeID = "9c9c9c9c-9c9c-9c9c-9c9c-9c9c9c9c9c9c"
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, $2, 'firm') ON CONFLICT (id) DO NOTHING`,
		probeID, "QA redeploy-safety probe",
	); err != nil {
		t.Fatalf("insert probe tenant (precondition): %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, probeID); err != nil {
			t.Errorf("cleanup probe tenant: %v", err)
		}
	})

	cfg := db.ProvisionConfig{
		Environment:   "development",
		BootstrapFlag: "true",
		SuperuserDSN:  superDSN,
		MigrationDSN:  migDSN,
		Passwords:     devRolePasswords(),
		BootstrapFS:   dbsql.FS,
		MigrationsFS:  migrations.FS,
		SeedFS:        dbsql.FS,
	}
	if err := db.Provision(ctx, cfg); err != nil {
		t.Fatalf("Provision (simulated redeploy) against a DB with pre-existing non-seed data: %v", err)
	}

	count := mustCount(t, pool, `SELECT count(*) FROM tenants WHERE id = $1`, probeID)
	if count != 1 {
		t.Fatalf("probe tenant: found %d row(s) after Provision, want exactly 1 — a redeploy must never destroy pre-existing data in the persistent development environment", count)
	}
}
