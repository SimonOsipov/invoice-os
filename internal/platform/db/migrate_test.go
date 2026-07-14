package db_test

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/pressly/goose/v3"

	db "github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

// TestMigrateUpFromEmbedded exercises the gateway's exact on-deploy path: the
// migrations embedded via go:embed apply cleanly from an empty schema through the
// shared MigrateUp helper, and nothing is left pending afterward. It is the
// embedded-FS analogue of the CI `migrations` job's filesystem round-trip and
// runs in that same job (DATABASE_MIGRATION_URL set, roles bootstrapped). It
// SKIPS without the migrator URL, so the pure-Go `go` job and a bare
// `go test ./...` stay green without a database.
//
// Beyond "does it apply", this guards a failure filesystem goose can't catch: a
// go:embed glob that ships a stale or incomplete set inside the gateway binary.
// Resetting to empty first proves every embedded migration is applied, not merely
// that the DB was already current.
//
// The DownTo(0) reset wipes the tenants the RLS harness (rls_harness_test.go) seeds
// in TestMain, so when that harness is active (a full-package run with all four
// DATABASE_* URLs) this test restores the shared fixtures it disturbs in a cleanup,
// keeping the package green un-filtered. In the CI `migrations` job only
// DATABASE_MIGRATION_URL is set, the harness self-skips, and the cleanup is a no-op.
func TestMigrateUpFromEmbedded(t *testing.T) {
	dsn := os.Getenv("DATABASE_MIGRATION_URL")
	if dsn == "" {
		t.Skip("DATABASE_MIGRATION_URL not set; skipping embedded-migration integration test")
	}
	ctx := context.Background()

	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open migrator connection: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		t.Fatalf("build migration provider: %v", err)
	}

	// DownTo(0) below rebuilds the schema from zero, wiping the tenants the RLS
	// harness seeded in TestMain. When the harness is active (full-package run with
	// all four DATABASE_* URLs) restore its fixtures afterwards so the other RLS
	// cases don't fail on missing tenants. In the CI `migrations` job only
	// DATABASE_MIGRATION_URL is set, so h == nil and this is a no-op — unchanged.
	if h != nil {
		t.Cleanup(func() {
			cctx := context.Background()
			if err := db.MigrateUp(cctx, dsn, migrations.FS); err != nil {
				t.Errorf("restore schema after migrate round-trip: %v", err)
				return
			}
			if err := h.restore(cctx); err != nil {
				t.Errorf("restore RLS harness fixtures: %v", err)
			}
		})
	}

	// Roll all the way back so Up is proven from zero. DownTo(0) runs every Down
	// in the embedded set — a stale/incomplete embed would already fail here.
	if _, err := provider.DownTo(ctx, 0); err != nil {
		t.Fatalf("reset to empty (down to 0): %v", err)
	}

	// The path under test: the shared helper the gateway calls at boot.
	if err := db.MigrateUp(ctx, dsn, migrations.FS); err != nil {
		t.Fatalf("MigrateUp from empty schema: %v", err)
	}

	// The schema is now non-empty (guards a vacuous pass — e.g. an empty embed
	// or a silently no-op MigrateUp).
	version, err := provider.GetDBVersion(ctx)
	if err != nil {
		t.Fatalf("get db version: %v", err)
	}
	if version == 0 {
		t.Fatalf("db version = 0 after MigrateUp, want the schema fully migrated")
	}

	// Nothing is pending: re-running Up applies zero migrations. Since Up applies
	// exactly what is pending, an empty result proves MigrateUp already applied
	// every embedded migration — not merely a subset.
	again, err := provider.Up(ctx)
	if err != nil {
		t.Fatalf("second Up: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second Up applied %d migration(s), want 0 — MigrateUp left work pending", len(again))
	}
}
