// M4-03-03 (task-104): tests for internal/importer's Store surface over the
// import_batches table, written BEFORE the real implementation exists (RED
// against the not-implemented stub bodies in store.go — see that file's doc
// comment). Store.CreateBatch/Finalize/ExistingNumbers/EntitySupplier wrap
// db.WithinRequestTenantTx exactly like internal/invoice.Store (System Design,
// M4-03-03 Implementation Plan) — RLS scopes tenant, so no manual
// `WHERE tenant_id` appears in any assertion query run through the app-role
// pool; the superuser pool is used only for seeding/reading-back rows
// out-of-band (bypasses RLS, so it needs no tenant context).
//
// This suite does NOT re-test cross-tenant RLS refusal on import_batches
// itself — internal/platform/db/import_batches_rls_test.go already covers 16
// TestRLS_ImportBatches* cases for that table. IB-STORE-04 below exercises a
// DIFFERENT RLS surface: ExistingNumbers queries the invoices table, so its
// tenant-scoping is a fresh Store-level concern (the [dedup-boundary]
// decision — a same-numbered invoice under another tenant/entity must never
// count as "already used" for the caller's own entity).
//
// Spec-to-test map (Test Specs table, M4-03-03 story / task-104):
//
//	IB-STORE-01 TestStoreCreateBatchFinalize_RoundTripsCountsStatusAndErrors
//	IB-STORE-02 TestStoreFinalize_EmptyErrorsMarshalsToEmptyArrayNotNull
//	IB-STORE-03 TestStoreExistingNumbers_ReturnsExactSubsetStoredForEntity
//	IB-STORE-04 TestStoreExistingNumbers_TenantScoped
//	IB-STORE-05 TestStoreEntitySupplier_ReturnsNameAndNilTIN
//	IB-STORE-06 TestStoreEntitySupplier_NotFoundOutsideTenant
//
// Run: `make test-rls` (or `make test-audit`), or directly, e.g.:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 ./internal/importer/...
package importer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- shared DB-test harness (copied per-package convention, per
// internal/invoice/store_test.go's dbTestPools/seedTenant/seedEntity idiom,
// itself copied from internal/portfolio/portfolio_test.go and
// internal/tenancy/tenancy_test.go — codebase convention is a per-package
// copy, not a cross-package import of test helpers) ------------------------

// dbTestPools returns the superuser (seed) and app-role (Store) pools for the
// importer db-integration suite below, or skips the test when the per-role
// DSNs are unset — copied from internal/invoice/store_test.go.
func dbTestPools(t *testing.T) (super, app *pgxpool.Pool) {
	t.Helper()
	appURL := os.Getenv("DATABASE_URL")
	superURL := os.Getenv("DATABASE_SUPERUSER_URL")
	if appURL == "" || superURL == "" {
		t.Skip("importer db-integration test skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL (or run `make test-rls`)")
	}
	ctx := context.Background()

	s, err := pgxpool.New(ctx, superURL)
	if err != nil {
		t.Fatalf("connect superuser: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Ping(ctx); err != nil {
		t.Fatalf("ping superuser (is the DB up and bootstrapped?): %v", err)
	}

	a, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatalf("connect app: %v", err)
	}
	t.Cleanup(a.Close)

	return s, a
}

// seedTenant inserts one throwaway tenants row (kind 'firm') as the
// superuser and registers a cleanup that deletes it. A tenants delete
// CASCADEs away every business_entities/invoices/import_batches row scoped
// to it, so per-test cleanup never has to unwind child rows by hand.
func seedTenant(t *testing.T, super *pgxpool.Pool, label string) string {
	t.Helper()
	ctx := context.Background()
	id := uuid.NewString()
	if _, err := super.Exec(ctx,
		`INSERT INTO tenants (id, name, kind) VALUES ($1, $2, 'firm')`, id, label,
	); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id)
	})
	return id
}

// seedEntity inserts one business_entities row for tenantID as the superuser
// (BYPASSRLS, so tin is left NULL) and registers its own cleanup
// (belt-and-suspenders alongside the tenant-cascade above; harmless once the
// tenant is gone).
func seedEntity(t *testing.T, super *pgxpool.Pool, tenantID, name string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := super.QueryRow(ctx,
		`INSERT INTO business_entities (tenant_id, name) VALUES ($1, $2) RETURNING id`,
		tenantID, name,
	).Scan(&id); err != nil {
		t.Fatalf("seed business_entities: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM business_entities WHERE id = $1`, id)
	})
	return id
}

// seedInvoice inserts one invoices row directly (bypassing internal/invoice's
// Store) as the superuser, for ExistingNumbers specs that need a known-good
// row to dedup against.
func seedInvoice(t *testing.T, super *pgxpool.Pool, tenantID, entityID, number string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := super.QueryRow(ctx,
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number) VALUES ($1, $2, $3) RETURNING id`,
		tenantID, entityID, number,
	).Scan(&id); err != nil {
		t.Fatalf("seed invoices: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM invoices WHERE id = $1`, id)
	})
	return id
}

// --- IB-STORE-01..06 --------------------------------------------------------

// IB-STORE-01: CreateBatch->Finalize round-trips counts and status; errors
// with entries (both the scalar-Row and plural-Rows shapes) marshal to the
// expected jsonb. RED against the stub: CreateBatch returns "" (no id), so
// the "id is empty" assertion fails before Finalize/read-back ever run.
func TestStoreCreateBatchFinalize_RoundTripsCountsStatusAndErrors(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "IB-STORE-01 tenant")
	entityID := seedEntity(t, super, tenantID, "IB-STORE-01 entity")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	id, err := store.CreateBatch(c, entityID)
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	if id == "" {
		t.Fatal("CreateBatch: id is empty, want a generated id")
	}

	errs := []RowError{
		{Row: 5, Message: "blank invoice_number"},
		{Rows: []int{7, 9}, Field: "total", Message: "rows disagree on total"},
	}
	if err := store.Finalize(c, id, 10, 8, 2, errs, "completed"); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	var status string
	var rowsTotal, rowsValid, rowsInvalid int
	var errorsJSON string
	if err := super.QueryRow(ctx,
		`SELECT status, rows_total, rows_valid, rows_invalid, errors::text FROM import_batches WHERE id = $1`,
		id,
	).Scan(&status, &rowsTotal, &rowsValid, &rowsInvalid, &errorsJSON); err != nil {
		t.Fatalf("read back import_batches row: %v", err)
	}

	if status != "completed" {
		t.Errorf("status = %q, want %q", status, "completed")
	}
	if rowsTotal != 10 || rowsValid != 8 || rowsInvalid != 2 {
		t.Errorf("counts = (%d,%d,%d), want (10,8,2)", rowsTotal, rowsValid, rowsInvalid)
	}

	var gotErrs []RowError
	if err := json.Unmarshal([]byte(errorsJSON), &gotErrs); err != nil {
		t.Fatalf("unmarshal errors jsonb %q: %v", errorsJSON, err)
	}
	if len(gotErrs) != 2 {
		t.Fatalf("errors array length = %d, want 2: %q", len(gotErrs), errorsJSON)
	}
	if gotErrs[0].Row != 5 || gotErrs[0].Message != "blank invoice_number" ||
		len(gotErrs[0].Rows) != 0 || gotErrs[0].Field != "" {
		t.Errorf("errors[0] = %+v, want {Row:5 Message:\"blank invoice_number\"}", gotErrs[0])
	}
	if len(gotErrs[1].Rows) != 2 || gotErrs[1].Rows[0] != 7 || gotErrs[1].Rows[1] != 9 ||
		gotErrs[1].Field != "total" || gotErrs[1].Message != "rows disagree on total" || gotErrs[1].Row != 0 {
		t.Errorf("errors[1] = %+v, want {Rows:[7 9] Field:\"total\" Message:\"rows disagree on total\"}", gotErrs[1])
	}
}

// IB-STORE-02: Finalize with nil (or empty) errs marshals the stored `errors`
// column to the jsonb empty array `[]`, never `null`. RED against the stub
// for the same reason as IB-STORE-01 — CreateBatch returns no id.
func TestStoreFinalize_EmptyErrorsMarshalsToEmptyArrayNotNull(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "IB-STORE-02 tenant")
	entityID := seedEntity(t, super, tenantID, "IB-STORE-02 entity")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	id, err := store.CreateBatch(c, entityID)
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	if id == "" {
		t.Fatal("CreateBatch: id is empty, want a generated id")
	}

	if err := store.Finalize(c, id, 0, 0, 0, nil, "completed"); err != nil {
		t.Fatalf("Finalize (nil errs): %v", err)
	}

	var errorsJSON string
	if err := super.QueryRow(ctx,
		`SELECT errors::text FROM import_batches WHERE id = $1`, id,
	).Scan(&errorsJSON); err != nil {
		t.Fatalf("read back errors: %v", err)
	}
	if errorsJSON != "[]" {
		t.Errorf("errors = %q, want %q (empty array, never null)", errorsJSON, "[]")
	}
}

// IB-STORE-03: ExistingNumbers returns exactly the subset of the queried
// numbers already stored for that entity — a number never stored (INV-C) is
// simply absent from the result, not present-with-false. RED against the
// stub: ExistingNumbers returns a nil map, so its length (0) fails the
// want-length-2 assertion.
func TestStoreExistingNumbers_ReturnsExactSubsetStoredForEntity(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "IB-STORE-03 tenant")
	entityID := seedEntity(t, super, tenantID, "IB-STORE-03 entity")
	seedInvoice(t, super, tenantID, entityID, "INV-A")
	seedInvoice(t, super, tenantID, entityID, "INV-B")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	got, err := store.ExistingNumbers(c, entityID, []string{"INV-A", "INV-B", "INV-C"})
	if err != nil {
		t.Fatalf("ExistingNumbers: %v", err)
	}

	want := map[string]bool{"INV-A": true, "INV-B": true}
	if len(got) != len(want) {
		t.Fatalf("ExistingNumbers len = %d, want %d: got %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("ExistingNumbers[%q] = %v, want %v", k, got[k], v)
		}
	}
	if got["INV-C"] {
		t.Errorf(`ExistingNumbers[%q] = true, want absent (never stored)`, "INV-C")
	}
}

// IB-STORE-04: ExistingNumbers is tenant-scoped — a same-numbered invoice
// under ANOTHER tenant's entity must never register as "already used" for
// the caller's own entity ([dedup-boundary]). Pairs a positive assertion
// (tenant A's own number is found) with the negative one (tenant B's
// identically-named number is not) so the test cannot vacuously pass on a
// nil/empty map — RED against the stub: it returns a nil map, so the
// positive "INV-A-OWN present" assertion fails.
func TestStoreExistingNumbers_TenantScoped(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantA := seedTenant(t, super, "IB-STORE-04 tenant A")
	tenantB := seedTenant(t, super, "IB-STORE-04 tenant B")
	entityA := seedEntity(t, super, tenantA, "IB-STORE-04 A entity")
	entityB := seedEntity(t, super, tenantB, "IB-STORE-04 B entity")

	seedInvoice(t, super, tenantA, entityA, "INV-A-OWN")
	seedInvoice(t, super, tenantB, entityB, "INV-A") // tenant B's own "INV-A"

	store := NewStore(app)
	cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})

	got, err := store.ExistingNumbers(cA, entityA, []string{"INV-A-OWN", "INV-A"})
	if err != nil {
		t.Fatalf("ExistingNumbers: %v", err)
	}
	if !got["INV-A-OWN"] {
		t.Errorf("ExistingNumbers[%q] = %v, want true (tenant A's own invoice)", "INV-A-OWN", got["INV-A-OWN"])
	}
	if got["INV-A"] {
		t.Errorf(`ExistingNumbers[%q] = true, want absent (belongs to tenant B, RLS-scoped out)`, "INV-A")
	}
	if len(got) != 1 {
		t.Errorf("ExistingNumbers len = %d, want 1: got %v", len(got), got)
	}
}

// IB-STORE-05: EntitySupplier returns (name, tin) for a tenant-visible entity
// whose tin is NULL — tin comes back as a nil *string, not a dereference
// panic or empty-string sentinel. RED against the stub: it always returns
// name="", which fails the want-entityName assertion.
func TestStoreEntitySupplier_ReturnsNameAndNilTIN(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "IB-STORE-05 tenant")
	const entityName = "IB-STORE-05 Supplier Co"
	entityID := seedEntity(t, super, tenantID, entityName)

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	name, tin, err := store.EntitySupplier(c, entityID)
	if err != nil {
		t.Fatalf("EntitySupplier: %v", err)
	}
	if name != entityName {
		t.Errorf("EntitySupplier name = %q, want %q", name, entityName)
	}
	if tin != nil {
		t.Errorf("EntitySupplier tin = %q, want nil", *tin)
	}
}

// IB-STORE-06: EntitySupplier on a uuid that resolves to zero rows under the
// caller's tenant (RLS-scoped, same as a genuinely nonexistent id) maps to
// ErrNotFound. RED against the stub: it always returns a nil error.
func TestStoreEntitySupplier_NotFoundOutsideTenant(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "IB-STORE-06 tenant")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	bogusEntityID := uuid.NewString()
	if _, _, err := store.EntitySupplier(c, bogusEntityID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("EntitySupplier(nonexistent id) err = %v, want ErrNotFound", err)
	}
}
