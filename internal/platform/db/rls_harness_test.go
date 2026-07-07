// The M2-07 adversarial RLS harness. This file stands up the shared fixtures the
// attack cases in rls_test.go build on; the cases themselves live there.
//
// Design (docs/migrations.md §6, §8):
//   - Reuses the SAME Postgres-service-container + Makefile-bootstrap path as the CI
//     `migrations` job (NOT testcontainers, which the build plan floated) — no new Go
//     dependency, one canonical role-bootstrap (db/bootstrap.sql), CI-consistent.
//   - One connection pool PER ROLE, because RLS is enforced by WHO you connect as: a
//     single pool could not exercise the owner-bypass or enumeration cases.
//   - Applied against a DB whose roles/tables were created by db/bootstrap.sql +
//     goose, so `tenants` is owned by invoice_migrator exactly as in prod (required
//     for the owner-bypass case to be meaningful).
//   - The suite SKIPS ITSELF when the per-role DATABASE_* URLs are absent, so a bare
//     `go test ./...` and the default CI `go` job stay green with no database. It runs
//     only under the CI `rls` job or `make test-rls`, which supply all four URLs.
package db_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// errNoDB signals the suite is not configured to run (a per-role URL is unset). It is
// NOT a failure — the suite skips. A URL that IS set but unreachable is a real error.
var errNoDB = errors.New("rls: DATABASE_* env not set")

// harness holds one pool per role plus the two tenants it seeds.
type harness struct {
	super  *pgxpool.Pool // superuser: seeds data + reads pg_roles (BYPASSRLS, so seeds ignore RLS)
	mig    *pgxpool.Pool // invoice_migrator: owns tenants + the fixture table (the owner-bypass target)
	app    *pgxpool.Pool // invoice_app: the NOBYPASSRLS runtime identity under test
	reader *pgxpool.Pool // invoice_tenant_reader: the one cross-tenant enumeration identity

	appURL string // the pool-reuse case needs its own single-connection pool

	tenantA string
	tenantB string
}

// h is the shared harness, nil when the suite is not configured.
var h *harness

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()
	var err error
	h, err = setupHarness(ctx)
	if err != nil {
		if errors.Is(err, errNoDB) {
			// Not configured: still run, so every case skips itself (requireHarness).
			return m.Run()
		}
		fmt.Fprintln(os.Stderr, "rls suite setup failed:", err)
		return 1
	}
	defer h.teardown(ctx)
	return m.Run()
}

// requireHarness skips the calling test when the suite is not configured.
func requireHarness(t *testing.T) *harness {
	t.Helper()
	if h == nil {
		t.Skip("RLS suite skipped: set DATABASE_URL, DATABASE_MIGRATION_URL, " +
			"DATABASE_SUPERUSER_URL and DATABASE_READER_URL (or run `make test-rls`)")
	}
	return h
}

func setupHarness(ctx context.Context) (*harness, error) {
	appURL := os.Getenv("DATABASE_URL")
	migURL := os.Getenv("DATABASE_MIGRATION_URL")
	superURL := os.Getenv("DATABASE_SUPERUSER_URL")
	readerURL := os.Getenv("DATABASE_READER_URL")
	if appURL == "" || migURL == "" || superURL == "" || readerURL == "" {
		return nil, errNoDB
	}

	hh := &harness{appURL: appURL}
	for _, c := range []struct {
		dst **pgxpool.Pool
		url string
		who string
	}{
		{&hh.super, superURL, "superuser"},
		{&hh.mig, migURL, "migrator"},
		{&hh.app, appURL, "app"},
		{&hh.reader, readerURL, "reader"},
	} {
		pool, err := pgxpool.New(ctx, c.url)
		if err != nil {
			return nil, fmt.Errorf("connect %s: %w", c.who, err)
		}
		*c.dst = pool
	}

	// A URL that is set but unreachable / not bootstrapped is a real error (e.g.
	// `make test-rls` without `make dev-db`), not a skip.
	if err := hh.super.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping superuser (is the DB up and bootstrapped?): %w", err)
	}

	hh.tenantA = uuid.NewString()
	hh.tenantB = uuid.NewString()

	if err := hh.createFixtureTable(ctx); err != nil {
		return nil, err
	}
	if err := hh.seed(ctx); err != nil {
		return nil, err
	}
	return hh, nil
}

// createFixtureTable builds the test-only tenant-scoped table (as the migrator/owner).
// It is the shape every M3+ table copies from the `tenants` template (the M2-06
// migration): ENABLE + FORCE RLS and a single isolation policy whose USING doubles as
// the INSERT/UPDATE WITH CHECK. Unlike `tenants` (which grants invoice_app SELECT
// only), it also grants invoice_app INSERT/UPDATE, so the app-role write path reaches
// the policy's WITH CHECK instead of being stopped by a missing table privilege. It is
// created and dropped at runtime — never a committed migration, so M2-07 ships no
// schema change. DROP-then-CREATE keeps it idempotent across reruns.
func (h *harness) createFixtureTable(ctx context.Context) error {
	stmts := []string{
		`DROP TABLE IF EXISTS rls_fixture`,
		`CREATE TABLE rls_fixture (
			id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id uuid NOT NULL,
			payload   text NOT NULL
		)`,
		`ALTER TABLE rls_fixture ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE rls_fixture FORCE  ROW LEVEL SECURITY`,
		`CREATE POLICY tenant_isolation ON rls_fixture
			USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid)`,
		`GRANT SELECT, INSERT, UPDATE ON rls_fixture TO invoice_app`,
	}
	for _, s := range stmts {
		if _, err := h.mig.Exec(ctx, s); err != nil {
			return fmt.Errorf("create fixture table: %w", err)
		}
	}
	return nil
}

// seed inserts the two tenants and one fixture row each, as the superuser — which has
// BYPASSRLS, so it writes both tables with no tenant context and no app INSERT grant
// (the same pattern as db/seed.dev.sql). Tenant ids are random per run to stay
// collision-free against any pre-existing rows in a shared dev DB.
func (h *harness) seed(ctx context.Context) error {
	if _, err := h.super.Exec(ctx,
		`INSERT INTO tenants (id, name) VALUES ($1, 'M2-07 tenant A'), ($2, 'M2-07 tenant B')`,
		h.tenantA, h.tenantB); err != nil {
		return fmt.Errorf("seed tenants: %w", err)
	}
	if _, err := h.super.Exec(ctx,
		`INSERT INTO rls_fixture (tenant_id, payload) VALUES ($1, 'a-row'), ($2, 'b-row')`,
		h.tenantA, h.tenantB); err != nil {
		return fmt.Errorf("seed fixture rows: %w", err)
	}
	return nil
}

// teardown drops the fixture table and this run's seeded tenants, then closes pools.
// Best-effort: the CI DB is ephemeral, and leftover rows (random ids) are harmless.
func (h *harness) teardown(ctx context.Context) {
	_, _ = h.mig.Exec(ctx, `DROP TABLE IF EXISTS rls_fixture`)
	_, _ = h.super.Exec(ctx, `DELETE FROM tenants WHERE id IN ($1, $2)`, h.tenantA, h.tenantB)
	h.reader.Close()
	h.app.Close()
	h.mig.Close()
	h.super.Close()
}

// querier is the read surface shared by pgx.Tx and *pgxpool.Pool, so mustCount works
// against either.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func mustCount(t *testing.T, q querier, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := q.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count query %q: %v", sql, err)
	}
	return n
}

// isRLSViolation reports whether err is Postgres's "new row violates row-level
// security policy" — SQLSTATE 42501, raised by an INSERT/UPDATE that fails a policy's
// WITH CHECK. That is the write-side refusal M2-07 asserts; read-side refusal is
// silent (the row is simply invisible), so those cases assert zero rows instead.
func isRLSViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42501"
}

func assertRLSViolation(t *testing.T, err error) {
	t.Helper()
	if !isRLSViolation(err) {
		t.Fatalf("want RLS WITH CHECK violation (SQLSTATE 42501), got %v", err)
	}
}
