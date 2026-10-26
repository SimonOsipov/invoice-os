package db_test

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/migrations"
)

// useScratchHarness points the package-global h at a fresh migrated scratch
// database, so plan tests see the planner stats of a fresh CI database rather
// than the grown dev one. It returns the scratch superuser DSN.
func useScratchHarness(t *testing.T) string {
	t.Helper()
	requireHarness(t)
	superDSN, migDSN := migratedScratchDB(t, migrations.FS)
	u, err := url.Parse(superDSN)
	if err != nil {
		t.Fatalf("parse scratch dsn: %v", err)
	}
	name := strings.TrimPrefix(u.Path, "/")

	scratch := &harness{appURL: dsnForDB(t, os.Getenv("DATABASE_URL"), name)}
	for _, c := range []struct {
		dst **pgxpool.Pool
		dsn string
	}{
		{&scratch.super, superDSN},
		{&scratch.mig, migDSN},
		{&scratch.app, scratch.appURL},
		{&scratch.reader, dsnForDB(t, os.Getenv("DATABASE_READER_URL"), name)},
	} {
		pool, err := pgxpool.New(context.Background(), c.dsn)
		if err != nil {
			t.Fatalf("connect scratch pool: %v", err)
		}
		*c.dst = pool
	}

	prev := h
	h = scratch
	t.Cleanup(func() {
		scratch.reader.Close()
		scratch.app.Close()
		scratch.mig.Close()
		scratch.super.Close()
		h = prev
	})
	return superDSN
}
