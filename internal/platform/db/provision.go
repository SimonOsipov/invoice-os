// provision.go — the gateway boot-time provisioning sequence (M4-21-04,
// task-128): bootstrap → migrate → seed, gated by BootstrapEnabled, all fatal on
// error and all complete before the gateway's listener opens (so a green
// /healthz continues to mean "fully provisioned"). See db.go for the package doc
// and bootstrap.go for the underlying BootstrapEnabled/Bootstrap/Seed primitives
// this composes; MigrateUp (migrate.go) is the third leg, unchanged.
//
// MODE-A STUB (task-128, Test-first: yes): this file exists only so
// internal/platform/db/provision_test.go — the architect's pre-authored Test
// Specs — compiles and fails on ASSERTIONS (the correct RED state for a
// test-first subtask) instead of on a missing symbol. Provision's real body is
// NOT implemented here; every declaration below is the smallest possible
// compilable stand-in (a correct name/signature/type, and a "not implemented"
// error), never a design decision beyond what's needed to compile. The executor
// replaces this per task-128's Implementation Plan and removes this notice.
//
// Design note for the executor: cmd/gateway/main.go is expected to call
// Provision (or the equivalent inline bootstrap→migrate→seed sequence) with
// Environment set to the RAW os.Getenv("ENVIRONMENT") — never
// app.Config.Environment, which substitutes "development" for an unset var
// (internal/platform/config.go:44) and would silently re-open the fail-open hole
// BootstrapEnabled's allowlist exists to close (QA F1, task-128's crux
// requirement — see TestProvisionGuardReadsRawEnvironment and
// TestGatewayMainPassesRawEnvironmentToProvisioningGuard in provision_test.go).
package db

import (
	"context"
	"errors"
	"io/fs"
)

// ProvisionConfig bundles everything Provision needs to run the boot-time
// bootstrap→migrate→seed sequence.
//
// Environment MUST be the raw os.Getenv("ENVIRONMENT") value (see
// BootstrapEnabled's doc comment) — Provision applies no substitution of its
// own, so passing app.Config.Environment here would defeat the allowlist guard
// one layer up. That call-site contract is what
// TestGatewayMainPassesRawEnvironmentToProvisioningGuard pins directly against
// cmd/gateway/main.go's source.
type ProvisionConfig struct {
	Environment   string // raw os.Getenv("ENVIRONMENT")
	BootstrapFlag string // raw os.Getenv("GATEWAY_DB_BOOTSTRAP")

	SuperuserDSN string // DATABASE_SUPERUSER_URL; read once per gated step, never retained (QA F3)
	MigrationDSN string // DATABASE_MIGRATION_URL; always required, guard-independent (unchanged gateway-as-migrator behavior)

	Passwords    RolePasswords // fiscalbridge.*_password GUCs Bootstrap sets
	BootstrapFS  fs.FS         // db/bootstrap.sql (db.FS)
	MigrationsFS fs.FS         // embedded migrations (migrations.FS)
	SeedFS       fs.FS         // db/seed.dev.sql (db.FS)
}

// Provision runs the gateway's boot-time provisioning sequence: bootstrap
// (gated by BootstrapEnabled) → migrate (unconditional — the existing
// gateway-as-migrator behavior, unchanged) → seed (gated), short-circuiting
// fatally on the first error so a partially provisioned database is never
// served. Bootstrap and Seed each open and close their own dedicated superuser
// connection (bootstrap.go); Provision retains no DSN or connection past the
// call that used it.
//
// STUB (Mode A, task-128): always returns a non-nil "not implemented" error and
// evaluates no guard, opens no connection, and runs no statement. See
// task-128's Implementation Plan for the real body (BootstrapEnabled(cfg.Environment,
// cfg.BootstrapFlag) gates Bootstrap and Seed around an unconditional MigrateUp).
func Provision(ctx context.Context, cfg ProvisionConfig) error {
	return errors.New("db: Provision not implemented (task-128)")
}
