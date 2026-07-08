// The M2-10 audit immutability/atomicity suite. It proves, against a real Postgres, that
// audit_log rows (a) commit or roll back atomically with the caller's transaction and
// (b) are append-only — no role, app or the table OWNER, can mutate or truncate one
// through ordinary SQL.
//
// Like the M2-07 RLS and M2-09 exactly-once suites, it reuses the Postgres-service-
// container + Makefile-bootstrap path (NOT testcontainers). Unlike those, audit_log is a
// COMMITTED table (the M2-10 migration), not a runtime fixture, so the cases run directly
// against the migrated schema. It needs DATABASE_URL (invoice_app — writes + the grant/RLS
// attacks) and DATABASE_MIGRATION_URL (invoice_migrator — the owner-can't-mutate attack,
// which grants alone cannot express), and SKIPS ITSELF when either is unset so a bare
// `go test ./...` and the default CI `go` job stay green. It runs under the CI `audit` job
// or `make test-audit`. Tenants are random uuids per case (audit_log has no FK to tenants).
package audit_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// --- shared fixture (TestMain) -------------------------------------------------------

// fixture holds the app pool (writes + grant/RLS attacks, as invoice_app) and the migrator
// pool (the owner attacks, as invoice_migrator). nil when the suite is not configured, so
// every case self-skips via requireFixture.
type fixture struct {
	app *pgxpool.Pool // invoice_app: the NOBYPASSRLS runtime role
	mig *pgxpool.Pool // invoice_migrator: the table OWNER (the owner-immutability target)
}

// fx is the shared fixture, nil when the suite is not configured.
var fx *fixture

// errRollback forces a WithinTenantTx to roll back for the atomicity case.
var errRollback = errors.New("audit: forced rollback")

func TestMain(m *testing.M) { os.Exit(runAudit(m)) }

func runAudit(m *testing.M) int {
	ctx := context.Background()
	appURL := os.Getenv("DATABASE_URL")
	migURL := os.Getenv("DATABASE_MIGRATION_URL")
	if appURL == "" || migURL == "" {
		// Not configured: still run so every case self-skips (requireFixture).
		return m.Run()
	}

	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		panic("audit suite: connect app pool: " + err.Error())
	}
	mig, err := pgxpool.New(ctx, migURL)
	if err != nil {
		app.Close()
		panic("audit suite: connect migrator pool: " + err.Error())
	}
	// A URL that is set but unreachable / not migrated is a real failure (e.g.
	// `make test-audit` without `make dev-db`), not a skip.
	if err := app.Ping(ctx); err != nil {
		app.Close()
		mig.Close()
		panic("audit suite: ping app DB (is it up, bootstrapped and migrated?): " + err.Error())
	}

	fx = &fixture{app: app, mig: mig}
	code := m.Run()
	app.Close()
	mig.Close()
	return code
}

// requireFixture skips the calling test when the suite is not configured.
func requireFixture(t *testing.T) *fixture {
	t.Helper()
	if fx == nil {
		t.Skip("audit suite skipped: set DATABASE_URL and DATABASE_MIGRATION_URL " +
			"(or run `make test-audit`)")
	}
	return fx
}

// --- acceptance cases ----------------------------------------------------------------

// AC #2: Record writes exactly one row in the caller's tx, with id/tenant_id/created_at
// defaulted and payload marshaled to jsonb; a nil payload is stored as {}.
func TestAudit_RecordInsertsRow(t *testing.T) {
	f := requireFixture(t)
	ctx := context.Background()
	tenant, actor, event := uuid.NewString(), uuid.NewString(), uuid.NewString()

	if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, actor, event, map[string]any{"k": "v", "n": 1})
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	var (
		id        int64
		gotTenant string
		gotActor  string
		payload   []byte
		createdAt time.Time
	)
	if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT id, tenant_id, actor, payload, created_at FROM audit_log WHERE event = $1`,
			event).Scan(&id, &gotTenant, &gotActor, &payload, &createdAt)
	}); err != nil {
		t.Fatalf("read back audit row: %v", err)
	}
	if id <= 0 {
		t.Errorf("id = %d, want a positive bigserial default", id)
	}
	if gotTenant != tenant {
		t.Errorf("tenant_id = %q, want %q (defaulted from the GUC)", gotTenant, tenant)
	}
	if gotActor != actor {
		t.Errorf("actor = %q, want %q", gotActor, actor)
	}
	if createdAt.IsZero() {
		t.Error("created_at is zero, want the now() default")
	}
	if string(payload) != `{"k": "v", "n": 1}` {
		t.Errorf("payload = %s, want the marshaled object", payload)
	}

	// nil payload → stored as the empty object {}, not JSON null.
	nilEvent := uuid.NewString()
	if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, actor, nilEvent, nil)
	}); err != nil {
		t.Fatalf("Record nil payload: %v", err)
	}
	var nilPayload []byte
	if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT payload FROM audit_log WHERE event = $1`, nilEvent).Scan(&nilPayload)
	}); err != nil {
		t.Fatalf("read back nil-payload row: %v", err)
	}
	if string(nilPayload) != "{}" {
		t.Errorf("nil payload stored as %s, want {}", nilPayload)
	}
}

// AC #3: an audit row shares the caller's transaction fate. A domain write (an
// idempotency_keys row — the app's other tenant-scoped writable table) plus audit.Record
// in one WithinTenantTx both vanish on rollback and both persist on commit.
func TestAudit_AtomicWithCallerTx(t *testing.T) {
	f := requireFixture(t)
	ctx := context.Background()
	tenant := uuid.NewString()

	// Rollback: neither the domain row nor the audit row survives.
	rbKey, rbEvent := uuid.NewString(), uuid.NewString()
	err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `INSERT INTO idempotency_keys (tenant_id, key) VALUES ($1, $2)`, tenant, rbKey); e != nil {
			return e
		}
		if e := audit.Record(ctx, tx, "actor", rbEvent, nil); e != nil {
			return e
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("WithinTenantTx err = %v, want errRollback", err)
	}
	if n := keyCount(t, f.app, tenant, rbKey); n != 0 {
		t.Errorf("idempotency_keys rows after rollback = %d, want 0", n)
	}
	if n := auditCount(t, f.app, tenant, rbEvent); n != 0 {
		t.Errorf("audit_log rows after rollback = %d, want 0", n)
	}

	// Commit: both persist.
	okKey, okEvent := uuid.NewString(), uuid.NewString()
	if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `INSERT INTO idempotency_keys (tenant_id, key) VALUES ($1, $2)`, tenant, okKey); e != nil {
			return e
		}
		return audit.Record(ctx, tx, "actor", okEvent, nil)
	}); err != nil {
		t.Fatalf("commit tx: %v", err)
	}
	if n := keyCount(t, f.app, tenant, okKey); n != 1 {
		t.Errorf("idempotency_keys rows after commit = %d, want 1", n)
	}
	if n := auditCount(t, f.app, tenant, okEvent); n != 1 {
		t.Errorf("audit_log rows after commit = %d, want 1", n)
	}
}

// AC #4: append-only for the app role — UPDATE and DELETE are rejected by the missing
// grant (SQLSTATE 42501) before RLS is even consulted, so no tenant context is needed.
func TestAudit_ImmutableToApp(t *testing.T) {
	f := requireFixture(t)
	ctx := context.Background()

	_, updErr := f.app.Exec(ctx, `UPDATE audit_log SET actor = 'tampered'`)
	assertSQLState(t, updErr, "42501")

	_, delErr := f.app.Exec(ctx, `DELETE FROM audit_log`)
	assertSQLState(t, delErr, "42501")
}

// AC #5: append-only even for the table OWNER. The migrator has full DML privilege on the
// table it owns, so 42501 cannot fire — the append-only trigger must. The row is seeded and
// the owner's UPDATE/DELETE runs with the matching tenant context set (via WithinTenantTx),
// so the row is actually matched and the trigger fires (restrict_violation, 23001) — proving
// the trigger, not RLS incidentally hiding the row, is what blocks the mutation.
func TestAudit_ImmutableToOwner(t *testing.T) {
	f := requireFixture(t)
	ctx := context.Background()
	tenant, event := uuid.NewString(), uuid.NewString()
	seedAudit(t, f.app, tenant, event)

	updErr := db.WithinTenantTx(ctx, f.mig, tenant, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE audit_log SET actor = 'tampered' WHERE event = $1`, event)
		return e
	})
	assertSQLState(t, updErr, "23001")

	delErr := db.WithinTenantTx(ctx, f.mig, tenant, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `DELETE FROM audit_log WHERE event = $1`, event)
		return e
	})
	assertSQLState(t, delErr, "23001")

	// The row is still there — neither mutation took effect.
	if n := auditCount(t, f.app, tenant, event); n != 1 {
		t.Errorf("audit rows after blocked owner mutation = %d, want 1", n)
	}
}

// AC #6: TRUNCATE is blocked by the statement-level trigger even for the owner (23001).
// TRUNCATE is not row-scoped by RLS, so the trigger is the only thing that can stop it.
func TestAudit_NoTruncate(t *testing.T) {
	f := requireFixture(t)
	ctx := context.Background()

	_, err := f.mig.Exec(ctx, `TRUNCATE audit_log`)
	assertSQLState(t, err, "23001")
}

// AC #7: tenant isolation — a row written under tenant A is invisible under tenant B.
func TestAudit_TenantIsolation(t *testing.T) {
	f := requireFixture(t)
	tenantA, tenantB, event := uuid.NewString(), uuid.NewString(), uuid.NewString()
	seedAudit(t, f.app, tenantA, event)

	if n := auditCount(t, f.app, tenantB, event); n != 0 {
		t.Errorf("tenant B sees %d of tenant A's audit rows, want 0", n)
	}
	// Non-vacuous: A sees its own row.
	if n := auditCount(t, f.app, tenantA, event); n != 1 {
		t.Errorf("tenant A sees %d of its own audit rows, want 1", n)
	}
}

// AC #8: the policy's USING doubles as the INSERT WITH CHECK — an explicit tenant_id that
// diverges from the current app.current_tenant is refused (42501), so a caller cannot forge
// a cross-tenant audit row even by writing tenant_id by hand.
func TestAudit_WithCheckRejectsForgedTenant(t *testing.T) {
	f := requireFixture(t)
	ctx := context.Background()
	tenantA, tenantB := uuid.NewString(), uuid.NewString()
	event := uuid.NewString()

	err := db.WithinTenantTx(ctx, f.app, tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO audit_log (tenant_id, actor, event) VALUES ($1, $2, $3)`,
			tenantB, "actor", event)
		return e
	})
	assertSQLState(t, err, "42501")
	if n := auditCount(t, f.app, tenantB, event); n != 0 {
		t.Errorf("forged cross-tenant audit rows = %d, want 0", n)
	}
}

// AC #9: fail-closed. An audit insert with NO tenant context set (a raw tx, not
// WithinTenantTx) defaults tenant_id to NULL, so the tenant-isolation WITH CHECK rejects it
// (42501) — the same policy that blocks cross-tenant writes also blocks context-less ones,
// and it fires before the column's NOT NULL backstop (a NULL tenant_id can never satisfy the
// policy, so 42501 is always the observed error). Either way: no unscoped audit row.
func TestAudit_FailClosedWithoutTenantContext(t *testing.T) {
	f := requireFixture(t)
	ctx := context.Background()

	tx, err := f.app.Begin(ctx) // deliberately NOT WithinTenantTx: no SET LOCAL app.current_tenant
	if err != nil {
		t.Fatalf("begin raw tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	recErr := audit.Record(ctx, tx, "actor", uuid.NewString(), nil)
	assertSQLState(t, recErr, "42501")
}

// --- helpers -------------------------------------------------------------------------

// seedAudit writes one committed audit row for tenant+event as the app role.
func seedAudit(t *testing.T, pool *pgxpool.Pool, tenant, event string) {
	t.Helper()
	if err := db.WithinTenantTx(context.Background(), pool, tenant, func(tx pgx.Tx) error {
		return audit.Record(context.Background(), tx, "seed-actor", event, nil)
	}); err != nil {
		t.Fatalf("seed audit row: %v", err)
	}
}

// auditCount counts audit_log rows for an event VISIBLE under tenant's context (FORCE RLS,
// so the count runs inside WithinTenantTx and sees only that tenant's rows).
func auditCount(t *testing.T, pool *pgxpool.Pool, tenant, event string) int {
	t.Helper()
	var n int
	if err := db.WithinTenantTx(context.Background(), pool, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM audit_log WHERE event = $1`, event).Scan(&n)
	}); err != nil {
		t.Fatalf("count audit_log: %v", err)
	}
	return n
}

// keyCount counts this tenant's idempotency_keys rows for key (the atomicity companion row).
func keyCount(t *testing.T, pool *pgxpool.Pool, tenant, key string) int {
	t.Helper()
	var n int
	if err := db.WithinTenantTx(context.Background(), pool, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM idempotency_keys WHERE key = $1`, key).Scan(&n)
	}); err != nil {
		t.Fatalf("count idempotency_keys: %v", err)
	}
	return n
}

// assertSQLState asserts err is a Postgres error carrying the given SQLSTATE.
func assertSQLState(t *testing.T, err error, want string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("want SQLSTATE %s, got non-Postgres err %v", want, err)
	}
	if pgErr.Code != want {
		t.Fatalf("want SQLSTATE %s, got %s (%s)", want, pgErr.Code, pgErr.Message)
	}
}
