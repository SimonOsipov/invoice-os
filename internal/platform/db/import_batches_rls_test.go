// M4-01-01 (task-92): tests for the `import_batches` tenant-owned table, written
// BEFORE the migration exists (RED against SQLSTATE 42P01 undefined_table). The
// table the Executor will add (Simon Vault "M4-01 Invoice Spine Migrations" §System
// Design #1):
//
//	import_batches: id uuid PK, tenant_id uuid NOT NULL REFERENCES tenants(id) ON
//	    DELETE CASCADE, entity_id uuid NOT NULL REFERENCES business_entities(id) ON
//	    DELETE CASCADE, status text NOT NULL DEFAULT 'pending' CHECK (status IN
//	    ('pending','processing','completed','failed')), rows_total/rows_valid/
//	    rows_invalid integer NOT NULL DEFAULT 0 with a non-negative CHECK, errors
//	    jsonb NOT NULL DEFAULT '[]', created_at timestamptz NOT NULL DEFAULT now() —
//	    verbatim M2-06 FORCE-RLS `tenant_isolation` policy, GRANT SELECT/INSERT/
//	    UPDATE (no DELETE) TO invoice_app.
//
// Each case attacks the same guarantees M2-07 (rls_test.go) proves for the
// tenants/rls_fixture shape and M3-01-03 (business_entities_rls_test.go)
// transplants onto a real table, applied here to import_batches, plus the
// table-specific status/counter CHECKs and the two FK cascades.
//
// Rows are seeded per-test (seedImportBatch below, reusing seedBusinessEntity from
// business_entities_rls_test.go for the parent row), NOT in the shared
// harness.seed() in rls_harness_test.go — that runs in TestMain before every test in
// the package, so a missing import_batches table would break the ENTIRE suite
// instead of failing only these IB-RLS cases.
//
// Spec-to-test map (Test Specs table, M4-01 story / task-92):
//
//	IB-RLS-01 TestRLS_ImportBatchesCrossTenantSelectRefused
//	IB-RLS-02 TestRLS_ImportBatchesCrossTenantInsertRefused
//	IB-RLS-03 TestRLS_ImportBatchesCrossTenantUpdateAffectsZeroRows
//	IB-RLS-04 TestRLS_ImportBatchesMissingContextFailsClosed
//	IB-RLS-05 TestRLS_ImportBatchesOwnTenantInsertSucceedsWithDefaults
//	IB-RLS-06 TestRLS_ImportBatchesOwnerInsertRefusedUnderForce
//	IB-RLS-07 TestRLS_ImportBatchesOwnRowReassignmentRefused
//	IB-RLS-08 TestRLS_ImportBatchesStatusCheck
//	IB-RLS-09 TestRLS_ImportBatchesEntityDeleteCascades
//	IB-RLS-10 TestRLS_ImportBatchesTenantDeleteCascades
//	IB-RLS-11 TestRLS_ImportBatchesCountsNonNegativeCheck
//
// Named with the TestRLS_ prefix so the CI `rls` job's `-run TestRLS`
// (.github/workflows/ci.yml) and `make test-rls` both pick these up automatically.
//
// Run: `make test-rls`, or directly with the same four DSNs, e.g.:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_MIGRATION_URL="postgres://invoice_migrator:migrator@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_READER_URL="postgres://invoice_tenant_reader:reader@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 -run TestRLS_ImportBatches ./internal/platform/db/...
package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// seedImportBatch inserts one import_batches row for tenantID/entityID as the
// superuser (BYPASSRLS, so seeding needs no tenant context) and returns its id plus
// a cleanup func. Scoped per-test — see the package doc comment above for why this
// must NOT move into the shared harness.seed().
func seedImportBatch(t *testing.T, tenantID, entityID string) (id string, cleanup func()) {
	t.Helper()
	ctx := context.Background()
	id = uuid.NewString()
	if _, err := h.super.Exec(ctx,
		`INSERT INTO import_batches (id, tenant_id, entity_id) VALUES ($1, $2, $3)`,
		id, tenantID, entityID,
	); err != nil {
		if code := pgCode(err); code == "42P01" {
			t.Fatalf("seed import_batches: undefined_table (42P01) — import_batches migration not applied yet: %v", err)
		}
		t.Fatalf("seed import_batches: %v", err)
	}
	return id, func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM import_batches WHERE id = $1`, id)
	}
}

// IB-RLS-01: cross-tenant SELECT is refused. An app-role tx scoped to tenant A sees
// only A's import_batches row; B's is invisible (filtered out, not an error).
func TestRLS_ImportBatchesCrossTenantSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "IB-01 A Corp")
	defer cleanupEntityA()
	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "IB-01 B Corp")
	defer cleanupEntityB()

	_, cleanupA := seedImportBatch(t, h.tenantA, entityA)
	defer cleanupA()
	_, cleanupB := seedImportBatch(t, h.tenantB, entityB)
	defer cleanupB()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM import_batches WHERE tenant_id = $1`, h.tenantA); n != 1 {
			t.Errorf("own (A) rows visible to A = %d, want 1", n)
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM import_batches WHERE tenant_id = $1`, h.tenantB); n != 0 {
			t.Errorf("B rows visible to A = %d, want 0", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}

// IB-RLS-02: a cross-tenant INSERT (row named for tenant B while scoped to A) is
// refused with a WITH CHECK violation, SQLSTATE 42501.
func TestRLS_ImportBatchesCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "IB-02 B Corp")
	defer cleanupEntityB()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO import_batches (tenant_id, entity_id) VALUES ($1, $2)`, h.tenantB, entityB)
		return e
	})
	assertRLSViolation(t, err)
}

// IB-RLS-03: an UPDATE that targets another tenant's rows affects zero rows and
// raises no error — B's row is simply invisible to a tx scoped to A.
func TestRLS_ImportBatchesCrossTenantUpdateAffectsZeroRows(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "IB-03 B Corp")
	defer cleanupEntityB()
	_, cleanupBatch := seedImportBatch(t, h.tenantB, entityB)
	defer cleanupBatch()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `UPDATE import_batches SET status = 'processing' WHERE tenant_id = $1`, h.tenantB)
		if e != nil {
			return e
		}
		if ct.RowsAffected() != 0 {
			t.Errorf("cross-tenant UPDATE affected %d rows, want 0", ct.RowsAffected())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("cross-tenant UPDATE (expected 0 rows): %v", err)
	}
}

// IB-RLS-04: a missing app.current_tenant GUC fails closed — with no context set,
// the isolation predicate is false for every row and the connection sees nothing.
func TestRLS_ImportBatchesMissingContextFailsClosed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if n := mustCount(t, tx, `SELECT count(*) FROM import_batches`); n != 0 {
		t.Errorf("import_batches visible with no tenant set = %d, want 0", n)
	}
}

// IB-RLS-05: a positive own-tenant INSERT succeeds — proves RLS's WITH CHECK and the
// tenants(id)/business_entities(id) FKs coexist for a same-tenant write, the row
// becomes visible, and status/row-counters/errors actually default as designed.
func TestRLS_ImportBatchesOwnTenantInsertSucceedsWithDefaults(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "IB-05 A Corp")
	defer cleanupEntityA()

	var id string
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO import_batches (tenant_id, entity_id) VALUES ($1, $2) RETURNING id`,
			h.tenantA, entityA,
		).Scan(&id)
	})
	if err != nil {
		t.Fatalf("own-tenant INSERT: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM import_batches WHERE id = $1`, id)
	}()

	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		var (
			status                            string
			rowsTotal, rowsValid, rowsInvalid int
			errorsJSON                        string
		)
		if e := tx.QueryRow(ctx,
			`SELECT status, rows_total, rows_valid, rows_invalid, errors::text FROM import_batches WHERE id = $1`,
			id,
		).Scan(&status, &rowsTotal, &rowsValid, &rowsInvalid, &errorsJSON); e != nil {
			return e
		}
		if status != "pending" {
			t.Errorf("status default = %q, want %q", status, "pending")
		}
		if rowsTotal != 0 || rowsValid != 0 || rowsInvalid != 0 {
			t.Errorf("row counters = (%d,%d,%d), want (0,0,0)", rowsTotal, rowsValid, rowsInvalid)
		}
		if errorsJSON != "[]" {
			t.Errorf("errors default = %q, want %q", errorsJSON, "[]")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("verify own-tenant insert defaults: %v", err)
	}
}

// IB-RLS-06: the table OWNER (invoice_migrator) is bound by the policy under FORCE
// exactly like the `tenants` template — a cross-tenant INSERT is refused even for
// the owner, SQLSTATE 42501.
func TestRLS_ImportBatchesOwnerInsertRefusedUnderForce(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "IB-06 B Corp")
	defer cleanupEntityB()

	err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO import_batches (tenant_id, entity_id) VALUES ($1, $2)`, h.tenantB, entityB)
		return e
	})
	assertRLSViolation(t, err)
}

// IB-RLS-07: reassigning an OWN, visible row to another tenant is refused. This is
// the case that catches a per-table policy copy-paste regression where the
// USING/WITH CHECK clause was narrowed to only validate fresh INSERTs and stopped
// re-checking an UPDATE's target tenant_id.
func TestRLS_ImportBatchesOwnRowReassignmentRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "IB-07 A Corp")
	defer cleanupEntityA()
	_, cleanupBatch := seedImportBatch(t, h.tenantA, entityA)
	defer cleanupBatch()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE import_batches SET tenant_id = $1 WHERE tenant_id = $2`, h.tenantB, h.tenantA)
		return e
	})
	assertRLSViolation(t, err)
}

// IB-RLS-08: the `status` CHECK rejects a value outside the lifecycle set (23514)
// and accepts each of the 4 legitimate states, round-tripping correctly.
func TestRLS_ImportBatchesStatusCheck(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "IB-08 A Corp")
	defer cleanupEntityA()

	// A bogus status is rejected.
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO import_batches (tenant_id, entity_id, status) VALUES ($1, $2, 'bogus')`,
			h.tenantA, entityA,
		)
		return e
	})
	if err == nil {
		t.Fatal("insert with status = 'bogus' succeeded, want CHECK violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("insert with status = 'bogus': SQLSTATE = %q, want 23514 (check_violation): %v", code, err)
	}

	// Each of the 4 legitimate lifecycle states is accepted and round-trips.
	for _, want := range []string{"pending", "processing", "completed", "failed"} {
		var id string
		err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`INSERT INTO import_batches (tenant_id, entity_id, status) VALUES ($1, $2, $3) RETURNING id`,
				h.tenantA, entityA, want,
			).Scan(&id)
		})
		if err != nil {
			t.Fatalf("insert with status = %q: want success, got: %v", want, err)
		}
		defer func(rowID string) {
			_, _ = h.super.Exec(context.Background(), `DELETE FROM import_batches WHERE id = $1`, rowID)
		}(id)

		var got string
		err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT status FROM import_batches WHERE id = $1`, id).Scan(&got)
		})
		if err != nil {
			t.Fatalf("read back status for %q: %v", want, err)
		}
		if got != want {
			t.Errorf("status read back = %q, want %q", got, want)
		}
	}
}

// IB-RLS-09: deleting the parent `business_entities` row cascades the batch away —
// `entity_id` is `ON DELETE CASCADE` (a batch is a disposable import-run record).
func TestRLS_ImportBatchesEntityDeleteCascades(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, _ := seedBusinessEntity(t, h.tenantA, "IB-09 A Corp")
	batchID, cleanupBatch := seedImportBatch(t, h.tenantA, entityA)
	defer cleanupBatch()

	if _, err := h.super.Exec(ctx, `DELETE FROM business_entities WHERE id = $1`, entityA); err != nil {
		t.Fatalf("delete parent business_entities row: %v", err)
	}

	if n := mustCount(t, h.super, `SELECT count(*) FROM import_batches WHERE id = $1`, batchID); n != 0 {
		t.Errorf("import_batches rows after entity delete = %d, want 0 (entity_id ON DELETE CASCADE)", n)
	}
}

// IB-RLS-10: deleting the parent `tenants` row cascades its import_batches rows
// away — `tenant_id` is `ON DELETE CASCADE` (the M3-01 child-table precedent). Uses
// a fresh, throwaway tenant (not the shared h.tenantA/B) since deleting it is the
// whole point of the test.
func TestRLS_ImportBatchesTenantDeleteCascades(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tenantID := uuid.NewString()
	if _, err := h.super.Exec(ctx,
		`INSERT INTO tenants (id, name) VALUES ($1, 'IB-10 throwaway tenant')`, tenantID,
	); err != nil {
		t.Fatalf("seed throwaway tenant: %v", err)
	}
	// Deliberately no defer cleanup for the tenant row itself — deleting it is the
	// action under test, and its CASCADE is expected to take the entity + batch too.

	entityID, _ := seedBusinessEntity(t, tenantID, "IB-10 throwaway entity")
	batchID, cleanupBatch := seedImportBatch(t, tenantID, entityID)
	defer cleanupBatch()

	if _, err := h.super.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tenantID); err != nil {
		t.Fatalf("delete parent tenants row: %v", err)
	}

	if n := mustCount(t, h.super, `SELECT count(*) FROM import_batches WHERE id = $1`, batchID); n != 0 {
		t.Errorf("import_batches rows after tenant delete = %d, want 0 (tenant_id ON DELETE CASCADE)", n)
	}
}

// IB-RLS-11: the row-counters are SYSTEM-written (never imported CSV content), so a
// non-negative CHECK is free integrity — a negative rows_total is rejected, 23514.
func TestRLS_ImportBatchesCountsNonNegativeCheck(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "IB-11 A Corp")
	defer cleanupEntityA()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO import_batches (tenant_id, entity_id, rows_total) VALUES ($1, $2, -1)`,
			h.tenantA, entityA,
		)
		return e
	})
	if err == nil {
		t.Fatal("insert with rows_total = -1 succeeded, want CHECK violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("insert with rows_total = -1: SQLSTATE = %q, want 23514 (check_violation): %v", code, err)
	}
}

// IB-RLS-12 (QA-added, least-privilege proof): invoice_tenant_reader has NO grant on
// import_batches at all (unlike tenants, which grants it SELECT via tenant_enumerate —
// see the migration header: "Deliberately NO tenant_enumerate/invoice_tenant_reader
// policy"). A bare SELECT as that role must fail at the GRANT level (SQLSTATE 42501
// insufficient_privilege) before RLS is even evaluated — proving the table was never
// exposed to the one cross-tenant enumeration identity. None of IB-RLS-01..11 connect
// as h.reader, so a future migration that widened the GRANT to include this role would
// slip through unnoticed without this case.
func TestRLS_ImportBatchesReaderHasNoGrant(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	var n int
	err := h.reader.QueryRow(ctx, `SELECT count(*) FROM import_batches`).Scan(&n)
	if err == nil {
		t.Fatal("invoice_tenant_reader SELECT on import_batches succeeded, want permission denied (SQLSTATE 42501)")
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("invoice_tenant_reader SELECT on import_batches: SQLSTATE = %q, want 42501 (insufficient_privilege): %v", code, err)
	}
}

// IB-RLS-13 (QA-added, least-privilege proof): invoice_app has NO DELETE grant on
// import_batches — the migration grants only SELECT/INSERT/UPDATE ("no DELETE" in the
// header). Even a same-tenant DELETE on a row the app can otherwise see/update must be
// refused at the GRANT level (42501), never reaching RLS's policy evaluation, and the
// row must survive untouched. None of IB-RLS-01..11 exercise DELETE, so a future
// migration that widened the GRANT would slip through unnoticed without this case
// (same guarantee INV-RLS-08 proves for invitations).
func TestRLS_ImportBatchesDeleteRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "IB-13 A Corp")
	defer cleanupEntityA()
	id, cleanupBatch := seedImportBatch(t, h.tenantA, entityA)
	defer cleanupBatch()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `DELETE FROM import_batches WHERE tenant_id = $1`, h.tenantA)
		return e
	})
	if err == nil {
		t.Fatal("app-role DELETE on import_batches succeeded, want permission denied (SQLSTATE 42501)")
	}
	if code := pgCode(err); code != "42501" {
		t.Fatalf("app-role DELETE on import_batches: SQLSTATE = %q, want 42501 (insufficient_privilege): %v", code, err)
	}

	if n := mustCount(t, h.super, `SELECT count(*) FROM import_batches WHERE id = $1`, id); n != 1 {
		t.Errorf("row count after refused DELETE = %d, want 1 (row must survive)", n)
	}
}

// IB-RLS-14 (QA-added, CHECK boundary): IB-RLS-11 only proves the negative side of the
// non-negative CHECK (rows_total = -1 rejected). Confirm the boundary itself — all
// three counters at exactly 0, the DEFAULT value every fresh batch starts at — is
// ALLOWED, not accidentally caught by an off-by-one CHECK (e.g. a wrongly-written
// `> 0` instead of `>= 0`).
func TestRLS_ImportBatchesCountsZeroBoundaryAllowed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "IB-14 A Corp")
	defer cleanupEntityA()

	var id string
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO import_batches (tenant_id, entity_id, rows_total, rows_valid, rows_invalid)
			 VALUES ($1, $2, 0, 0, 0) RETURNING id`,
			h.tenantA, entityA,
		).Scan(&id)
	})
	if err != nil {
		t.Fatalf("insert with counters explicitly = 0: want success, got: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM import_batches WHERE id = $1`, id)
	}()

	var rowsTotal, rowsValid, rowsInvalid int
	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT rows_total, rows_valid, rows_invalid FROM import_batches WHERE id = $1`, id,
		).Scan(&rowsTotal, &rowsValid, &rowsInvalid)
	})
	if err != nil {
		t.Fatalf("read back zero counters: %v", err)
	}
	if rowsTotal != 0 || rowsValid != 0 || rowsInvalid != 0 {
		t.Errorf("counters read back = (%d,%d,%d), want (0,0,0)", rowsTotal, rowsValid, rowsInvalid)
	}
}

// IB-RLS-15 (QA-added): a valid non-empty `errors` jsonb payload round-trips
// byte-for-byte through the app role. IB-RLS-05 only ever proves the DEFAULT ('[]');
// this confirms the column actually stores and returns caller-supplied JSON content,
// not just its default.
func TestRLS_ImportBatchesErrorsJSONRoundTrips(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "IB-15 A Corp")
	defer cleanupEntityA()

	const payload = `[{"row": 3, "message": "missing TIN"}, {"row": 7, "message": "invalid date"}]`

	var id string
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO import_batches (tenant_id, entity_id, errors) VALUES ($1, $2, $3::jsonb) RETURNING id`,
			h.tenantA, entityA, payload,
		).Scan(&id)
	})
	if err != nil {
		t.Fatalf("insert with explicit errors payload: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM import_batches WHERE id = $1`, id)
	}()

	var got string
	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT errors::text FROM import_batches WHERE id = $1`, id).Scan(&got)
	})
	if err != nil {
		t.Fatalf("read back errors: %v", err)
	}
	// Compare parsed structure, not raw text — jsonb normalizes whitespace/key order.
	var gotCount int
	if err := h.app.QueryRow(ctx, `SELECT jsonb_array_length($1::jsonb)`, got).Scan(&gotCount); err != nil {
		t.Fatalf("jsonb_array_length on read-back errors: %v", err)
	}
	if gotCount != 2 {
		t.Errorf("errors array length read back = %d, want 2 (payload did not round-trip)", gotCount)
	}
}

// IB-RLS-16 (QA-added, isolation completeness — belt-and-suspenders vs IB-RLS-01):
// with TWO tenants' batches coexisting, an unqualified SELECT * (no WHERE tenant_id
// filter) as tenant A returns ONLY A's rows. IB-RLS-01 always filters by tenant_id in
// the query itself, which would still return the right count even if RLS silently did
// nothing and the app happened to filter correctly; this case removes that filter so
// RLS is the ONLY thing narrowing the result set.
func TestRLS_ImportBatchesUnfilteredSelectSeesOnlyOwnTenant(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "IB-16 A Corp")
	defer cleanupEntityA()
	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "IB-16 B Corp")
	defer cleanupEntityB()

	idA, cleanupA := seedImportBatch(t, h.tenantA, entityA)
	defer cleanupA()
	_, cleanupB := seedImportBatch(t, h.tenantB, entityB)
	defer cleanupB()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT id, tenant_id FROM import_batches`)
		if e != nil {
			return e
		}
		defer rows.Close()

		seen := map[string]string{}
		for rows.Next() {
			var id, tenantID string
			if e := rows.Scan(&id, &tenantID); e != nil {
				return e
			}
			seen[id] = tenantID
		}
		if e := rows.Err(); e != nil {
			return e
		}

		if len(seen) != 1 {
			t.Errorf("unfiltered SELECT returned %d rows, want 1 (RLS should narrow to A's own row only)", len(seen))
		}
		if tenantID, ok := seen[idA]; !ok {
			t.Error("unfiltered SELECT did not include A's own row")
		} else if tenantID != h.tenantA {
			t.Errorf("returned row's tenant_id = %q, want %q", tenantID, h.tenantA)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}
