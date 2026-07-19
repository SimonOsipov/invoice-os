// [M4-22-08 follow-up] Coverage for Provision's named-variable DSN validation.
//
// Motivation, from a real incident rather than a hypothetical: on 2026-07-19 the
// gateway crash-looped in a freshly forked PR environment with
//
//	provision: migrate: apply migrations: failed to connect to
//	`user=invoice_migrator database=railway`: failed SASL auth:
//	FATAL: password authentication failed for user "invoice_migrator" (28P01)
//
// because DATABASE_MIGRATION_URL still interpolated a Railway variable that had
// been deleted, so it rendered as `postgresql://invoice_migrator:@host/db` — a
// well-formed DSN carrying an EMPTY password. Railway reported the whole thing
// only as "service unavailable" for the healthcheck window.
//
// validateDSN closes the two adjacent, cheaply-detectable variants of that
// class: a DSN that is empty outright, and one that still contains an
// unrendered `${{` reference. Both fail BEFORE any dial, naming the environment
// variable an operator must go fix rather than the credential it carries.
//
// HONEST SCOPE — read before extending: validateDSN would NOT have caught the
// incident above. `postgresql://invoice_migrator:@host/db` is non-empty and
// contains no `${{`, so it passes validation and still fails at the wire with
// 28P01. Catching an empty *password component* would mean parsing the DSN and
// deciding that a passwordless DSN is always wrong — which it is not (peer/trust
// auth and PGPASSWORD both make it legitimate). These tests therefore pin what
// validation actually promises, and deliberately do not overclaim it.
//
// All cases are pure logic: ConnectWait is left zero (waitForPostgres
// short-circuits), and validation runs before any connection, so no test here
// performs network I/O. Poison DSN sentinels are reused from provision_test.go
// (same package) to prove, by their ABSENCE from the error, that no dial was
// attempted.
package db_test

import (
	"context"
	"strings"
	"testing"

	dbsql "github.com/SimonOsipov/invoice-os/db"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

// unrenderedDSN is what a Railway variable reference looks like when the
// variable it points at does not resolve in the environment: the reference text
// survives verbatim into the rendered value.
const unrenderedDSN = "postgresql://invoice_migrator:${{MIGRATOR_PASSWORD}}@postgres.railway.internal:5432/railway"

func provisionCfg(env, superuser, migration string) db.ProvisionConfig {
	return db.ProvisionConfig{
		Environment:   env,
		BootstrapFlag: "true",
		SuperuserDSN:  superuser,
		MigrationDSN:  migration,
		Passwords:     db.RolePasswords{Migrator: "m-pw", App: "a-pw", Reader: "r-pw"},
		BootstrapFS:   dbsql.FS,
		MigrationsFS:  migrations.FS,
		SeedFS:        dbsql.FS,
	}
}

// An empty DATABASE_MIGRATION_URL must fail naming that variable, and must fail
// before anything is dialed. Without the check, pgx receives "" and falls back
// to libpq defaults, reporting a connection failure against the process's OS
// user and an empty database — an error naming nothing the operator can act on.
func TestProvisionEmptyMigrationDSNNamesTheVariable(t *testing.T) {
	err := db.Provision(context.Background(), provisionCfg("production", superuserPoisonDSN, ""))
	if err == nil {
		t.Fatal("Provision returned nil with an empty MigrationDSN — want a named failure")
	}
	if !strings.Contains(err.Error(), "DATABASE_MIGRATION_URL") {
		t.Errorf("Provision error = %q, want it to name DATABASE_MIGRATION_URL", err.Error())
	}
	if strings.Contains(err.Error(), superuserPoisonDSN) {
		t.Errorf("Provision error = %q, mentions the superuser DSN marker — validation must fail before any dial", err.Error())
	}
}

// A DSN still carrying `${{` is an unrendered reference: the variable EXISTS on
// the service (so a bare non-empty check passes) but the value it points at did
// not resolve. This is the shape M4-22's password rename left behind.
func TestProvisionUnrenderedMigrationDSNNamesTheVariable(t *testing.T) {
	err := db.Provision(context.Background(), provisionCfg("production", superuserPoisonDSN, unrenderedDSN))
	if err == nil {
		t.Fatal("Provision returned nil with an unrendered MigrationDSN — want a named failure")
	}
	if !strings.Contains(err.Error(), "DATABASE_MIGRATION_URL") {
		t.Errorf("Provision error = %q, want it to name DATABASE_MIGRATION_URL", err.Error())
	}
	if !strings.Contains(err.Error(), "UNRENDERED") {
		t.Errorf("Provision error = %q, want it to say the reference is UNRENDERED — an operator seeing only 'invalid DSN' would go looking at the wrong layer", err.Error())
	}
	// The credential must not be echoed; the variable name is the actionable part.
	if strings.Contains(err.Error(), "MIGRATOR_PASSWORD") {
		t.Errorf("Provision error = %q, echoes the referenced secret's name from the DSN value — report the variable, not the value", err.Error())
	}
}

// With the bootstrap guard ON the superuser DSN is dialed first, so it is
// validated too — and named on its own terms rather than surfacing as a generic
// bootstrap failure.
func TestProvisionEmptySuperuserDSNNamesTheVariableWhenGuardOn(t *testing.T) {
	err := db.Provision(context.Background(), provisionCfg("development", "", migrationPoisonDSN))
	if err == nil {
		t.Fatal("Provision returned nil with guard ON and an empty SuperuserDSN — want a named failure")
	}
	if !strings.Contains(err.Error(), "DATABASE_SUPERUSER_URL") {
		t.Errorf("Provision error = %q, want it to name DATABASE_SUPERUSER_URL", err.Error())
	}
	if strings.Contains(err.Error(), migrationPoisonDSN) {
		t.Errorf("Provision error = %q, mentions the migration DSN marker — superuser validation must short-circuit before migrate", err.Error())
	}
}

// The mirror of the case above, and the one that keeps production bootable: with
// the guard OFF, DATABASE_SUPERUSER_URL is legitimately unset and must NOT be
// validated. Regressing this would fail-fast every production boot — a far worse
// outcome than the blind failure the validation exists to prevent.
func TestProvisionGuardOffDoesNotRequireSuperuserDSN(t *testing.T) {
	err := db.Provision(context.Background(), provisionCfg("production", "", migrationPoisonDSN))
	if err == nil {
		t.Fatal("Provision returned nil — want the migrate step to fail against the poison DSN")
	}
	if strings.Contains(err.Error(), "DATABASE_SUPERUSER_URL") {
		t.Fatalf("Provision error = %q, validated the superuser DSN with the guard OFF — production boots legitimately without it", err.Error())
	}
	if !strings.Contains(err.Error(), migrationPoisonDSN) {
		t.Errorf("Provision error = %q, want proof migrate was still reached and dialed the migration DSN", err.Error())
	}
}
