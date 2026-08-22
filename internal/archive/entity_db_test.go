// entity_db_test.go: RED specs for AUDIT-05-03 (Mode A) -- selectEntity against a real
// Postgres. Rollback-wrapped harness: one superuser tx per test plants fixtures
// (bypasses RLS), then SET LOCAL ROLE invoice_app + app.current_tenant in the SAME tx
// before calling the function under test, then rolls back -- zero residue, no VACUUM
// needed (contrast internal/audit/audit_plan_test.go, which commits on purpose).
// Needs DATABASE_URL and DATABASE_SUPERUSER_URL (not DATABASE_MIGRATION_URL -- no
// migration runs here). package archive (white-box), matching request_test.go.
package archive

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func dbSuperPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" || os.Getenv("DATABASE_SUPERUSER_URL") == "" {
		t.Skip("archive db-integration test skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL (or run `make test-archive`)")
	}
	pool, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_SUPERUSER_URL"))
	if err != nil {
		t.Fatalf("connect superuser: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("ping superuser (is the DB up and bootstrapped?): %v", err)
	}
	return pool
}

// beginFixtureTx opens one superuser transaction and rolls it back on cleanup. Every
// fixture insert and every call to the function under test in a given test shares this
// same tx, so an uncommitted fixture is visible to the query that follows it.
func beginFixtureTx(t *testing.T, super *pgxpool.Pool) pgx.Tx {
	t.Helper()
	tx, err := super.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin superuser tx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}

// actingAs switches the (still-open) superuser tx to invoice_app and sets the tenant
// GUC, both SET LOCAL so neither survives past this transaction. Call only after every
// superuser fixture insert -- RLS applies to invoice_app, not to postgres.
func actingAs(t *testing.T, tx pgx.Tx, tenantID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE invoice_app`); err != nil {
		t.Fatalf("SET LOCAL ROLE invoice_app: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, tenantID); err != nil {
		t.Fatalf("set app.current_tenant=%s: %v", tenantID, err)
	}
}

// mustCreateTenant inserts as superuser -- invoice_app holds SELECT-only on tenants,
// so even a same-tenant fixture can't mint its own tenant row.
func mustCreateTenant(t *testing.T, tx pgx.Tx, name string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := tx.Exec(context.Background(), `INSERT INTO tenants (id, name) VALUES ($1, $2)`, id, name); err != nil {
		t.Fatalf("insert tenant fixture: %v", err)
	}
	return id
}

func TestSelectEntity_UnknownIDIsErrEntityNotFound(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-entity-unknown")
	actingAs(t, tx, tenant)

	_, err := selectEntity(context.Background(), tx, uuid.NewString())
	if !errors.Is(err, ErrEntityNotFound) {
		t.Errorf("selectEntity(id matching no row) error = %v, want ErrEntityNotFound", err)
	}
}

// AC-2: "does not exist" and "belongs to another tenant" must be indistinguishable.
func TestRLS_SelectEntityOfAnotherTenantIsErrEntityNotFound(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenantA := mustCreateTenant(t, tx, "archive-entity-tenant-a")
	tenantB := mustCreateTenant(t, tx, "archive-entity-tenant-b")
	entityA := mustCreateEntity(t, tx, tenantA, "Tenant A Co", "12345678-0001")

	// Control needle (superuser, pre-actingAs): the fixture really planted the row.
	var planted int
	if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM business_entities WHERE id = $1`, entityA).Scan(&planted); err != nil {
		t.Fatalf("control-needle count: %v", err)
	}
	if planted != 1 {
		t.Fatalf("control needle: entityA row count = %d, want 1 -- fixture setup is broken", planted)
	}

	actingAs(t, tx, tenantB)
	_, err := selectEntity(context.Background(), tx, entityA)
	if !errors.Is(err, ErrEntityNotFound) {
		t.Errorf("selectEntity(another tenant's entity id) error = %v, want ErrEntityNotFound "+
			"(AC-2: must read the same as a nonexistent id)", err)
	}
}

func TestSelectEntity_ReturnsNameAndTinForTheFilename(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-entity-honeywell")
	entityID := mustCreateEntity(t, tx, tenant, "Honeywell Group", "98765432-0001")

	actingAs(t, tx, tenant)
	got, err := selectEntity(context.Background(), tx, entityID)
	if err != nil {
		t.Fatalf("selectEntity: unexpected error: %v", err)
	}
	if got.ID != entityID || got.Name != "Honeywell Group" || got.TIN == nil || *got.TIN != "98765432-0001" {
		t.Errorf("selectEntity = %+v, want ID=%q Name=%q TIN=%q", got, entityID, "Honeywell Group", "98765432-0001")
	}
}

// The urn:uuid: regression this subtask closes: uuid.Parse (and subtask 01's shipped
// TestParseRequest_NonCanonicalUUIDFormsAcceptedRaw) accept this form and keep
// Request.EntityID raw, but Postgres's uuid_in rejects it (SQLSTATE 22P02) unless
// selectEntity normalizes at the DB boundary via normalizeEntityID.
func TestSelectEntity_URNFormEntityIDReachesTheEntity(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-entity-urn")
	entityID := mustCreateEntity(t, tx, tenant, "URN Form Co", "11111111-0001")

	actingAs(t, tx, tenant)
	urnForm := "urn:uuid:" + entityID
	got, err := selectEntity(context.Background(), tx, urnForm)
	if err != nil {
		t.Fatalf("selectEntity(%q) = error %v, want the entity reached (the urn:uuid: regression this subtask closes)", urnForm, err)
	}
	if got.ID != entityID {
		t.Errorf("selectEntity(%q).ID = %q, want %q", urnForm, got.ID, entityID)
	}
}
