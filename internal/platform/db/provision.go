// provision.go — the gateway boot-time provisioning sequence (M4-21-04,
// task-128): bootstrap → migrate → seed, gated by BootstrapEnabled, all fatal on
// error and all complete before the gateway's listener opens (so a green /healthz
// continues to mean "fully provisioned"). See db.go for the package doc and
// bootstrap.go for the underlying BootstrapEnabled/Bootstrap/Seed primitives this
// composes; MigrateUp (migrate.go) is the third leg, unchanged.
//
// cmd/gateway/main.go calls Provision with Environment set to the RAW
// os.Getenv("ENVIRONMENT") — never app.Config.Environment, which substitutes
// "development" for an unset var (internal/platform/config.go:44) and would
// silently re-open the fail-open hole BootstrapEnabled's allowlist exists to
// close (QA F1, task-128's crux requirement — see
// TestProvisionGuardReadsRawEnvironment and
// TestGatewayMainPassesRawEnvironmentToProvisioningGuard in provision_test.go).
package db

import (
	"context"
	"fmt"
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
// The guard is evaluated exactly once, from cfg.Environment/cfg.BootstrapFlag —
// never re-read from the process environment — so bootstrap and seed are
// gated identically even though migrate runs unconditionally between them.
// When the guard is false, cfg.Passwords/cfg.SuperuserDSN are never even
// looked at: Bootstrap/Seed are not called at all, so an empty/zero
// RolePasswords or an unreachable superuser DSN cannot fail a guard-off boot
// (AC-3).
func Provision(ctx context.Context, cfg ProvisionConfig) error {
	enabled := BootstrapEnabled(cfg.Environment, cfg.BootstrapFlag)

	if enabled {
		if err := Bootstrap(ctx, cfg.SuperuserDSN, cfg.Passwords, cfg.BootstrapFS); err != nil {
			return fmt.Errorf("db: provision: bootstrap: %w", err)
		}
	}

	// Unconditional and unchanged: the gateway is the fleet's single in-network
	// migrator regardless of whether deploy-time provisioning is enabled for
	// this environment (docs/migrations.md §2).
	if err := MigrateUp(ctx, cfg.MigrationDSN, cfg.MigrationsFS); err != nil {
		return fmt.Errorf("db: provision: migrate: %w", err)
	}

	if enabled {
		if err := Seed(ctx, cfg.SuperuserDSN, cfg.SeedFS); err != nil {
			return fmt.Errorf("db: provision: seed: %w", err)
		}
	}

	return nil
}
