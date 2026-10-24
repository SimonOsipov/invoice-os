// Coverage for Provision's named-variable DSN validation: a DSN that is empty
// outright, and one that still contains an unrendered `${{` reference. Both fail
// BEFORE any dial, naming the environment variable an operator must go fix
// rather than the credential it carries.
//
// HONEST SCOPE — read before extending: validateDSN does NOT catch an empty
// password. `postgresql://invoice_migrator:@host/db` passes validation and fails
// at the wire with 28P01. A passwordless DSN is legitimate under peer/trust auth
// and PGPASSWORD, so these tests pin only what validation promises.
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

// unrenderedDSN is what a template reference looks like when the
// variable it points at does not resolve in the environment: the reference text
// survives verbatim into the rendered value.
const unrenderedDSN = "postgresql://invoice_migrator:${{MIGRATOR_PASSWORD}}@postgres.internal:5432/invoice_os"

func provisionCfg(env, superuser, migration string) db.ProvisionConfig {
	return db.ProvisionConfig{
		Environment:   env,
		BootstrapFlag: "true",
		SuperuserDSN:  superuser,
		MigrationDSN:  migration,
		Passwords:     db.RolePasswords{Migrator: "m-pw", App: "a-pw", Reader: "r-pw", AuthAdmin: "aa-pw"},
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
