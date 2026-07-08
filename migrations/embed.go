// Package migrations embeds the SQL migration files so a Go binary can apply
// them at deploy time without shipping the goose binary or the loose .sql files
// alongside it. This is what lets the gateway be the in-network migrator
// (docs/migrations.md §2): its distroless image contains only the compiled
// binary, so the migrations have to travel *inside* it.
//
// The on-disk files under migrations/ remain the single source of truth —
// `go tool goose` (the Makefile targets and the CI `migrations` job) reads them
// straight from disk. This embed just makes the exact same files available to
// the compiled binary via internal/platform/db.MigrateUp.
package migrations

import "embed"

// FS holds every migrations/*.sql file, embedded into the binary. goose reads
// them from the root of this FS — goose.NewProvider(dialect, db, migrations.FS).
// (A `//go:embed *.sql` that matched no files would fail the build, so this can
// never silently embed an empty set.)
//
//go:embed *.sql
var FS embed.FS
