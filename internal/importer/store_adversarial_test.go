// M4-03-03 (task-104) Stage 4 QA: adversarial Store coverage added
// post-implementation. The 6 IB-STORE-01..06 acceptance-criteria tests in
// store_test.go cover the story's ACs; this file pins behavior they don't
// exercise: a bogus entity_id on CreateBatch (FK violation, no orphan row), the
// import_batches status/counts CHECK constraints on Finalize ([status-mapping]
// — only pending|processing|completed|failed are writable, and counts must
// stay non-negative), a positive same-tenant round-trip of CreateBatch's
// tenant_id through the APP pool (complementing the cross-tenant REFUSAL cases
// already covered by internal/platform/db/import_batches_rls_test.go's 16
// TestRLS_ImportBatches* cases), a non-nil-TIN EntitySupplier case (positive
// complement to IB-STORE-05's nil-TIN case), and ExistingNumbers given
// duplicate/crafted-string inputs (dedup + parameterized-query safety).
//
// This suite does NOT re-test cross-tenant RLS refusal on import_batches
// itself -- see store_test.go's header comment and
// internal/platform/db/import_batches_rls_test.go.
package importer

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// CreateBatch with a syntactically-valid uuid that names no business_entities
// row (in this tenant or any other) must map to ErrValidation via the
// foreign_key_violation (23503) path -- never a 500/panic -- and must not
// leave a committed import_batches row behind. The super pool (BYPASSRLS)
// confirms zero rows for the tenant afterward, since the app-pool Store
// itself cannot see rows it never committed either way.
func TestStoreCreateBatch_BogusEntityIDReturnsValidationNoOrphanRow(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "adversarial bogus-entity tenant")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	bogusEntityID := uuid.NewString() // well-formed uuid, no such business_entities row
	id, err := store.CreateBatch(c, bogusEntityID)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("CreateBatch(bogus entity_id) err = %v, want ErrValidation", err)
	}
	if id != "" {
		t.Errorf("CreateBatch(bogus entity_id) id = %q, want empty on error", id)
	}

	var count int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM import_batches WHERE tenant_id = $1`, tenantID,
	).Scan(&count); err != nil {
		t.Fatalf("count import_batches for tenant: %v", err)
	}
	if count != 0 {
		t.Errorf("import_batches rows for tenant after failed CreateBatch = %d, want 0 (no orphan batch committed)", count)
	}
}

// Finalize with a status outside the import_batches CHECK's four values
// (pending|processing|completed|failed) must be rejected by Postgres
// (check_violation, 23514) -- a caller can never silently write a bogus
// status, pinning [status-mapping]'s claim that only those four are writable.
func TestStoreFinalize_InvalidStatusRejectedByCheckConstraint(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "adversarial bad-status tenant")
	entityID := seedEntity(t, super, tenantID, "adversarial bad-status entity")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	id, err := store.CreateBatch(c, entityID)
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}

	err = store.Finalize(c, id, 1, 1, 0, nil, "partial")
	if err == nil {
		t.Fatal(`Finalize(status="partial") err = nil, want the status CHECK (pending|processing|completed|failed) to reject it`)
	}
	if code := pgCode(err); code != "23514" {
		t.Errorf(`Finalize(status="partial") pgCode = %q, want %q (check_violation): err=%v`, code, "23514", err)
	}
}

// Finalize with a negative row count must be rejected by the
// import_batches_counts_non_negative CHECK (23514) -- these counters are
// system-written, never imported content, so there is no legitimate negative
// value to pass through.
func TestStoreFinalize_NegativeCountRejectedByCheckConstraint(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "adversarial negative-count tenant")
	entityID := seedEntity(t, super, tenantID, "adversarial negative-count entity")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	id, err := store.CreateBatch(c, entityID)
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}

	err = store.Finalize(c, id, 5, 5, -1, nil, "failed")
	if err == nil {
		t.Fatal("Finalize(rowsInvalid=-1) err = nil, want the import_batches_counts_non_negative CHECK to reject a negative count")
	}
	if code := pgCode(err); code != "23514" {
		t.Errorf("Finalize(rowsInvalid=-1) pgCode = %q, want %q (check_violation): err=%v", code, "23514", err)
	}
}

// Positive complement to the cross-tenant REFUSAL cases already covered by
// import_batches_rls_test.go: CreateBatch's tenant_id write must be readable
// back through the APP pool (not just the superuser pool) under the SAME
// tenant's identity -- proving CreateBatch wrote the correct tenant_id and
// that the caller can see its own batch, not just that other tenants can't.
func TestStoreCreateBatch_VisibleViaAppPoolSameTenant(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "adversarial app-pool round-trip tenant")
	entityID := seedEntity(t, super, tenantID, "adversarial app-pool round-trip entity")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	id, err := store.CreateBatch(c, entityID)
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}

	var gotTenantID string
	err = db.WithinRequestTenantTx(c, app, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT tenant_id::text FROM import_batches WHERE id = $1`, id).Scan(&gotTenantID)
	})
	if err != nil {
		t.Fatalf("read batch back through the APP pool as its own tenant %s: %v (CreateBatch's tenant_id write, or the caller's own-tenant visibility, is broken)", tenantID, err)
	}
	if gotTenantID != tenantID {
		t.Errorf("batch tenant_id (read via app pool) = %q, want %q", gotTenantID, tenantID)
	}
}

// Positive complement to IB-STORE-05 (which pins the nil-TIN case):
// EntitySupplier on an entity WITH a non-null tin returns it as a non-nil
// *string with the exact stored value.
func TestStoreEntitySupplier_ReturnsNameAndNonNilTIN(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "adversarial tin tenant")
	const entityName = "adversarial TIN Supplier Co"
	const entityTIN = "1234567890"

	var entityID string
	if err := super.QueryRow(ctx,
		`INSERT INTO business_entities (tenant_id, name, tin) VALUES ($1, $2, $3) RETURNING id`,
		tenantID, entityName, entityTIN,
	).Scan(&entityID); err != nil {
		t.Fatalf("seed business_entities with tin: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, entityID)
	})

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	name, tin, err := store.EntitySupplier(c, entityID)
	if err != nil {
		t.Fatalf("EntitySupplier: %v", err)
	}
	if name != entityName {
		t.Errorf("EntitySupplier name = %q, want %q", name, entityName)
	}
	if tin == nil {
		t.Fatal("EntitySupplier tin = nil, want a non-nil *string")
	}
	if *tin != entityTIN {
		t.Errorf("EntitySupplier tin = %q, want %q", *tin, entityTIN)
	}
}

// ExistingNumbers given a duplicate input entry collapses it to one map key
// (map semantics, not a query error), and given an input containing quotes/
// commas/SQL-metacharacters treats it as ordinary bound data -- proving the
// $2 = ANY(...) parameterization is not vulnerable to string-built SQL. The
// crafted string is never stored, so it must simply be absent from the
// result; the invoices row count for the tenant is checked afterward to
// confirm no injected statement executed.
func TestStoreExistingNumbers_DuplicateAndSpecialCharacterInputsHandledSafely(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "adversarial dup/special tenant")
	entityID := seedEntity(t, super, tenantID, "adversarial dup/special entity")
	seedInvoice(t, super, tenantID, entityID, "INV-A")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	crafted := `INV,"B'; DROP TABLE invoices;--`
	got, err := store.ExistingNumbers(c, entityID, []string{"INV-A", "INV-A", crafted})
	if err != nil {
		t.Fatalf("ExistingNumbers(duplicate + special-character input): %v, want no error (parameterized query, not string-built SQL)", err)
	}
	if len(got) != 1 {
		t.Fatalf("ExistingNumbers len = %d, want 1 (duplicate INV-A collapses to one key, crafted string never stored): got %v", len(got), got)
	}
	if !got["INV-A"] {
		t.Errorf(`ExistingNumbers[%q] = %v, want true`, "INV-A", got["INV-A"])
	}
	if got[crafted] {
		t.Errorf("ExistingNumbers[crafted string] = true, want absent (never stored)")
	}

	var stillThere int
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM invoices WHERE tenant_id = $1`, tenantID,
	).Scan(&stillThere); err != nil {
		t.Fatalf("post-check invoices count: %v", err)
	}
	if stillThere != 1 {
		t.Errorf("invoices count for tenant after crafted-string ExistingNumbers call = %d, want 1 (no injection occurred)", stillThere)
	}
}

// Finalize on a batch id that doesn't exist at all (or that RLS makes
// invisible under the caller's tenant -- same observable effect: 0 rows
// match the WHERE) must return ErrNotFound rather than silently succeeding
// on 0 affected rows (CodeRabbit finding, M4-03 PR review) -- a caller must
// never be told a batch was finalized when nothing was actually updated.
func TestStoreFinalize_UnknownIDReturnsNotFoundNoRowsAffected(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "adversarial finalize-not-found tenant")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	err := store.Finalize(c, uuid.NewString(), 1, 1, 0, nil, "completed")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Finalize(unseeded id) err = %v, want ErrNotFound", err)
	}
}
