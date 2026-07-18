// Package dbsql embeds db/bootstrap.sql and db/seed.dev.sql so a Go binary can
// execute them at boot (internal/platform/db.Bootstrap / .Seed, M4-21-03) with no
// on-disk file present in the image — mirroring migrations/embed.go's rationale
// exactly: the gateway's distroless image contains only the compiled binary, so
// these files have to travel *inside* it.
//
// The on-disk files under db/ remain the single source of truth — psql, `make
// db-bootstrap` and `make dev-db` read them straight from disk. This embed just
// makes the identical bytes available to the compiled binary.
//
// NOTE: `go:embed` cannot reach outside its own package directory, which is why
// this embed lives here in db/ rather than in internal/platform/db.
package dbsql

import "embed"

// FS holds db/bootstrap.sql and db/seed.dev.sql, embedded into the binary. A glob
// that failed to match either file would fail the build, so this can never
// silently ship a stale or incomplete copy — see TestBootstrapFromEmbedded /
// TestSeedFromEmbeddedIsIdempotent (internal/platform/db), which additionally
// prove the embedded bytes are complete/correct at runtime, not merely present.
//
//go:embed bootstrap.sql seed.dev.sql
var FS embed.FS
