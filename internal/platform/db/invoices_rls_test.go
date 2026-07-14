// M4-01-02 (task-93): tests for the `invoices` tenant-owned table, written BEFORE
// the migration exists (RED against SQLSTATE 42P01 undefined_table). The table the
// Executor will add (Simon Vault "M4-01 Invoice Spine Migrations" §System Design #2):
//
//	invoices: id uuid PK, tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE
//	    CASCADE, entity_id uuid NOT NULL REFERENCES business_entities(id) ON DELETE
//	    RESTRICT (durability — an invoice is a legal record, must survive a portfolio
//	    hard-delete), import_batch_id uuid REFERENCES import_batches(id) ON DELETE
//	    SET NULL, invoice_number text NOT NULL (identity), status text NOT NULL
//	    DEFAULT 'draft' CHECK (status IN ('draft','validated','queued','submitted',
//	    'accepted','rejected','failed')). MBS-content columns (issue_date,
//	    supplier_tin/name, buyer_tin/name, currency, subtotal/vat/total numeric(14,2))
//	    are NULLABLE and un-CHECKed — store-invalid, so M4-04 can report violations
//	    instead of the schema hard-rejecting an invalid import. violations jsonb NOT
//	    NULL DEFAULT '[]', rule_set_version_id uuid REFERENCES rule_set_versions(id)
//	    (nullable, NO ACTION), created_at timestamptz NOT NULL DEFAULT now() —
//	    verbatim M2-06 FORCE-RLS `tenant_isolation` policy, UNIQUE (tenant_id,
//	    entity_id, invoice_number) hard guard, GRANT SELECT/INSERT/UPDATE (no DELETE)
//	    TO invoice_app.
//
// Each of INV-RLS-01..07 attacks the same guarantees M2-07 (rls_test.go) proves for
// the tenants/rls_fixture shape and M3-01-03 (business_entities_rls_test.go) /
// M4-01-01 (import_batches_rls_test.go) transplant onto real tables, applied here to
// invoices. INV-RLS-08..16 are table-specific: the unique guard, the status CHECK,
// the store-invalid guarantee, the two FK dispositions (import_batch_id SET NULL,
// entity_id RESTRICT), the rule_set_version_id FK, and the D8 cross-tenant
// dangling-reference residual (documented, not defended — see the story's QA-Verify
// disposition [2]).
//
// Rows are seeded per-test (seedInvoice below, reusing seedBusinessEntity from
// business_entities_rls_test.go and seedImportBatch from import_batches_rls_test.go
// for parent rows), NOT in the shared harness.seed() in rls_harness_test.go — that
// runs in TestMain before every test in the package, so a missing invoices table
// would break the ENTIRE suite instead of failing only these INV-RLS cases.
//
// Spec-to-test map (Test Specs table, M4-01 story / task-93):
//
//	INV-RLS-01 TestRLS_InvoicesCrossTenantSelectRefused
//	INV-RLS-02 TestRLS_InvoicesCrossTenantInsertRefused
//	INV-RLS-03 TestRLS_InvoicesCrossTenantUpdateAffectsZeroRows
//	INV-RLS-04 TestRLS_InvoicesMissingContextFailsClosed
//	INV-RLS-05 TestRLS_InvoicesOwnTenantInsertSucceedsWithDefaults
//	INV-RLS-06 TestRLS_InvoicesOwnerInsertRefusedUnderForce
//	INV-RLS-07 TestRLS_InvoicesOwnRowReassignmentRefused
//	INV-RLS-08 TestRLS_InvoicesUniqueGuardDuplicateRejected
//	INV-RLS-09 TestRLS_InvoicesUniqueGuardDifferentEntityAllowed
//	INV-RLS-10 TestRLS_InvoicesUniqueGuardDifferentTenantAllowed
//	INV-RLS-11 TestRLS_InvoicesStatusCheck
//	INV-RLS-12 TestRLS_InvoicesStoreInvalidDraftSucceeds
//	INV-RLS-13 TestRLS_InvoicesImportBatchDeleteSetsNull
//	INV-RLS-14 TestRLS_InvoicesRuleSetVersionFK
//	INV-RLS-15 TestRLS_InvoicesCrossTenantDanglingEntityRef
//	INV-RLS-16 TestRLS_InvoicesEntityDeleteRestricted
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
//	go test -count=1 -run TestRLS_Invoices ./internal/platform/db/...
package db_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// seedInvoice inserts one invoices row for tenantID/entityID/invoiceNumber as the
// superuser (BYPASSRLS, so seeding needs no tenant context) and returns its id plus a
// cleanup func. Scoped per-test — see the package doc comment above for why this must
// NOT move into the shared harness.seed().
func seedInvoice(t *testing.T, tenantID, entityID, invoiceNumber string) (id string, cleanup func()) {
	t.Helper()
	ctx := context.Background()
	id = uuid.NewString()
	if _, err := h.super.Exec(ctx,
		`INSERT INTO invoices (id, tenant_id, entity_id, invoice_number) VALUES ($1, $2, $3, $4)`,
		id, tenantID, entityID, invoiceNumber,
	); err != nil {
		if code := pgCode(err); code == "42P01" {
			t.Fatalf("seed invoices: undefined_table (42P01) — invoices migration not applied yet: %v", err)
		}
		t.Fatalf("seed invoices: %v", err)
	}
	return id, func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM invoices WHERE id = $1`, id)
	}
}

// INV-RLS-01: cross-tenant SELECT is refused. An app-role tx scoped to tenant A sees
// only A's invoices row; B's is invisible (filtered out, not an error).
func TestRLS_InvoicesCrossTenantSelectRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "INV-01 A Corp")
	defer cleanupEntityA()
	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "INV-01 B Corp")
	defer cleanupEntityB()

	_, cleanupA := seedInvoice(t, h.tenantA, entityA, "INV-01-A")
	defer cleanupA()
	_, cleanupB := seedInvoice(t, h.tenantB, entityB, "INV-01-B")
	defer cleanupB()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		if n := mustCount(t, tx, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, h.tenantA); n != 1 {
			t.Errorf("own (A) rows visible to A = %d, want 1", n)
		}
		if n := mustCount(t, tx, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, h.tenantB); n != 0 {
			t.Errorf("B rows visible to A = %d, want 0", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTenantTx: %v", err)
	}
}

// INV-RLS-02: a cross-tenant INSERT (row named for tenant B while scoped to A) is
// refused with a WITH CHECK violation, SQLSTATE 42501.
func TestRLS_InvoicesCrossTenantInsertRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "INV-02 B Corp")
	defer cleanupEntityB()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO invoices (tenant_id, entity_id, invoice_number) VALUES ($1, $2, 'INV-02-ROGUE')`,
			h.tenantB, entityB,
		)
		return e
	})
	assertRLSViolation(t, err)
}

// INV-RLS-03: an UPDATE that targets another tenant's rows affects zero rows and
// raises no error — B's row is simply invisible to a tx scoped to A.
func TestRLS_InvoicesCrossTenantUpdateAffectsZeroRows(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "INV-03 B Corp")
	defer cleanupEntityB()
	_, cleanupInvoice := seedInvoice(t, h.tenantB, entityB, "INV-03-B")
	defer cleanupInvoice()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `UPDATE invoices SET status = 'validated' WHERE tenant_id = $1`, h.tenantB)
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

// INV-RLS-04: a missing app.current_tenant GUC fails closed — with no context set,
// the isolation predicate is false for every row and the connection sees nothing.
func TestRLS_InvoicesMissingContextFailsClosed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	tx, err := h.app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if n := mustCount(t, tx, `SELECT count(*) FROM invoices`); n != 0 {
		t.Errorf("invoices visible with no tenant set = %d, want 0", n)
	}
}

// INV-RLS-05: a positive own-tenant INSERT succeeds — proves RLS's WITH CHECK and the
// tenants(id)/business_entities(id) FKs coexist for a same-tenant write, the row
// becomes visible, and status/violations/rule_set_version_id/import_batch_id actually
// default as designed (draft / [] / NULL / NULL).
func TestRLS_InvoicesOwnTenantInsertSucceedsWithDefaults(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "INV-05 A Corp")
	defer cleanupEntityA()

	var id string
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO invoices (tenant_id, entity_id, invoice_number) VALUES ($1, $2, 'INV-05-A') RETURNING id`,
			h.tenantA, entityA,
		).Scan(&id)
	})
	if err != nil {
		t.Fatalf("own-tenant INSERT: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM invoices WHERE id = $1`, id)
	}()

	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		var (
			status         string
			violationsJSON string
			ruleSetVersion *string
			importBatch    *string
		)
		if e := tx.QueryRow(ctx,
			`SELECT status, violations::text, rule_set_version_id, import_batch_id FROM invoices WHERE id = $1`,
			id,
		).Scan(&status, &violationsJSON, &ruleSetVersion, &importBatch); e != nil {
			return e
		}
		if status != "draft" {
			t.Errorf("status default = %q, want %q", status, "draft")
		}
		if violationsJSON != "[]" {
			t.Errorf("violations default = %q, want %q", violationsJSON, "[]")
		}
		if ruleSetVersion != nil {
			t.Errorf("rule_set_version_id default = %v, want NULL", *ruleSetVersion)
		}
		if importBatch != nil {
			t.Errorf("import_batch_id default = %v, want NULL", *importBatch)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("verify own-tenant insert defaults: %v", err)
	}
}

// INV-RLS-06: the table OWNER (invoice_migrator) is bound by the policy under FORCE
// exactly like the `tenants` template — a cross-tenant INSERT is refused even for the
// owner, SQLSTATE 42501.
func TestRLS_InvoicesOwnerInsertRefusedUnderForce(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "INV-06 B Corp")
	defer cleanupEntityB()

	err := db.WithinTenantTx(ctx, h.mig, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO invoices (tenant_id, entity_id, invoice_number) VALUES ($1, $2, 'INV-06-ROGUE')`,
			h.tenantB, entityB,
		)
		return e
	})
	assertRLSViolation(t, err)
}

// INV-RLS-07: reassigning an OWN, visible row to another tenant is refused. This is
// the case that catches a per-table policy copy-paste regression where the
// USING/WITH CHECK clause was narrowed to only validate fresh INSERTs and stopped
// re-checking an UPDATE's target tenant_id.
func TestRLS_InvoicesOwnRowReassignmentRefused(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "INV-07 A Corp")
	defer cleanupEntityA()
	_, cleanupInvoice := seedInvoice(t, h.tenantA, entityA, "INV-07-A")
	defer cleanupInvoice()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE invoices SET tenant_id = $1 WHERE tenant_id = $2`, h.tenantB, h.tenantA)
		return e
	})
	assertRLSViolation(t, err)
}

// INV-RLS-08: the unique guard UNIQUE (tenant_id, entity_id, invoice_number) rejects
// a second row with the same triple, SQLSTATE 23505.
func TestRLS_InvoicesUniqueGuardDuplicateRejected(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "INV-08 A Corp")
	defer cleanupEntityA()

	_, cleanupFirst := seedInvoice(t, h.tenantA, entityA, "INV-08-DUP")
	defer cleanupFirst()

	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO invoices (tenant_id, entity_id, invoice_number) VALUES ($1, $2, 'INV-08-DUP')`,
			h.tenantA, entityA,
		)
		return e
	})
	if err == nil {
		t.Fatal("duplicate (tenant, entity, invoice_number) succeeded, want unique_violation (SQLSTATE 23505)")
	}
	if code := pgCode(err); code != "23505" {
		t.Fatalf("duplicate (tenant, entity, invoice_number): SQLSTATE = %q, want 23505 (unique_violation): %v", code, err)
	}
}

// INV-RLS-09: the SAME invoice_number under a DIFFERENT entity, same tenant, is
// allowed — the guard is per (tenant, entity), not per tenant alone.
func TestRLS_InvoicesUniqueGuardDifferentEntityAllowed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA1, cleanupEntityA1 := seedBusinessEntity(t, h.tenantA, "INV-09 A Corp 1")
	defer cleanupEntityA1()
	entityA2, cleanupEntityA2 := seedBusinessEntity(t, h.tenantA, "INV-09 A Corp 2")
	defer cleanupEntityA2()

	_, cleanupFirst := seedInvoice(t, h.tenantA, entityA1, "INV-09-SHARED")
	defer cleanupFirst()

	var secondID string
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO invoices (tenant_id, entity_id, invoice_number) VALUES ($1, $2, 'INV-09-SHARED') RETURNING id`,
			h.tenantA, entityA2,
		).Scan(&secondID)
	})
	if err != nil {
		t.Fatalf("same invoice_number under a different entity (same tenant): want success, got: %v", err)
	}
	_, _ = h.super.Exec(context.Background(), `DELETE FROM invoices WHERE id = $1`, secondID)
}

// INV-RLS-10: the SAME invoice_number under a DIFFERENT tenant is allowed — the
// guard is scoped per tenant, not global.
func TestRLS_InvoicesUniqueGuardDifferentTenantAllowed(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "INV-10 A Corp")
	defer cleanupEntityA()
	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "INV-10 B Corp")
	defer cleanupEntityB()

	_, cleanupFirst := seedInvoice(t, h.tenantA, entityA, "INV-10-SHARED")
	defer cleanupFirst()

	var secondID string
	err := db.WithinTenantTx(ctx, h.app, h.tenantB, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO invoices (tenant_id, entity_id, invoice_number) VALUES ($1, $2, 'INV-10-SHARED') RETURNING id`,
			h.tenantB, entityB,
		).Scan(&secondID)
	})
	if err != nil {
		t.Fatalf("same invoice_number under a different tenant: want success, got: %v", err)
	}
	_, _ = h.super.Exec(context.Background(), `DELETE FROM invoices WHERE id = $1`, secondID)
}

// INV-RLS-11: the `status` CHECK rejects a value outside the 7-state lifecycle set
// (23514), accepts each of the 7 canonical states round-tripping correctly, and the
// DEFAULT (no status named) reads back 'draft'.
func TestRLS_InvoicesStatusCheck(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "INV-11 A Corp")
	defer cleanupEntityA()

	// A bogus status is rejected.
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO invoices (tenant_id, entity_id, invoice_number, status) VALUES ($1, $2, 'INV-11-BOGUS', 'bogus')`,
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

	// Each of the 7 canonical states is accepted and round-trips.
	for i, want := range []string{"draft", "validated", "queued", "submitted", "accepted", "rejected", "failed"} {
		number := fmt.Sprintf("INV-11-%d", i)
		var id string
		err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`INSERT INTO invoices (tenant_id, entity_id, invoice_number, status) VALUES ($1, $2, $3, $4) RETURNING id`,
				h.tenantA, entityA, number, want,
			).Scan(&id)
		})
		if err != nil {
			t.Fatalf("insert with status = %q: want success, got: %v", want, err)
		}
		defer func(rowID string) {
			_, _ = h.super.Exec(context.Background(), `DELETE FROM invoices WHERE id = $1`, rowID)
		}(id)

		var got string
		err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, id).Scan(&got)
		})
		if err != nil {
			t.Fatalf("read back status for %q: %v", want, err)
		}
		if got != want {
			t.Errorf("status read back = %q, want %q", got, want)
		}
	}

	// The DEFAULT (no status named on INSERT) reads back 'draft'.
	var defaultID string
	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO invoices (tenant_id, entity_id, invoice_number) VALUES ($1, $2, 'INV-11-DEFAULT') RETURNING id`,
			h.tenantA, entityA,
		).Scan(&defaultID)
	})
	if err != nil {
		t.Fatalf("insert without status (want default 'draft'): %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM invoices WHERE id = $1`, defaultID)
	}()

	var defaultStatus string
	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, defaultID).Scan(&defaultStatus)
	})
	if err != nil {
		t.Fatalf("read back default status: %v", err)
	}
	if defaultStatus != "draft" {
		t.Errorf("default status = %q, want %q", defaultStatus, "draft")
	}
}

// INV-RLS-12: store-invalid. An invalid draft — negative subtotal, NULL currency,
// NULL supplier_tin, NULL issue_date — INSERTs successfully because MBS-content
// columns carry no CHECK (D2): the import→validate→fix loop requires an invalid row
// be storable so M4-04 can later report *why* it is invalid.
func TestRLS_InvoicesStoreInvalidDraftSucceeds(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "INV-12 A Corp")
	defer cleanupEntityA()

	var id string
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO invoices (tenant_id, entity_id, invoice_number, subtotal, currency, supplier_tin, issue_date)
			 VALUES ($1, $2, 'INV-12-INVALID', -5, NULL, NULL, NULL) RETURNING id`,
			h.tenantA, entityA,
		).Scan(&id)
	})
	if err != nil {
		t.Fatalf("insert invalid draft (negative subtotal, NULL currency/supplier_tin/issue_date): want success (store-invalid, no content CHECK), got: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM invoices WHERE id = $1`, id)
	}()

	var (
		subtotal    string
		currency    *string
		supplierTIN *string
		issueDate   *string
	)
	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT subtotal::text, currency, supplier_tin, issue_date::text FROM invoices WHERE id = $1`, id,
		).Scan(&subtotal, &currency, &supplierTIN, &issueDate)
	})
	if err != nil {
		t.Fatalf("read back invalid draft: %v", err)
	}
	if subtotal != "-5.00" {
		t.Errorf("subtotal read back = %q, want %q", subtotal, "-5.00")
	}
	if currency != nil {
		t.Errorf("currency read back = %q, want NULL", *currency)
	}
	if supplierTIN != nil {
		t.Errorf("supplier_tin read back = %q, want NULL", *supplierTIN)
	}
	if issueDate != nil {
		t.Errorf("issue_date read back = %q, want NULL", *issueDate)
	}
}

// INV-RLS-13: import_batch_id is ON DELETE SET NULL. Deleting the parent
// import_batches row nulls the invoice's import_batch_id; the invoice itself
// survives (it is the durable record, the batch is disposable — D7).
func TestRLS_InvoicesImportBatchDeleteSetsNull(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "INV-13 A Corp")
	defer cleanupEntityA()
	batchID, cleanupBatch := seedImportBatch(t, h.tenantA, entityA)
	defer cleanupBatch()

	invoiceID := uuid.NewString()
	if _, err := h.super.Exec(ctx,
		`INSERT INTO invoices (id, tenant_id, entity_id, invoice_number, import_batch_id) VALUES ($1, $2, $3, 'INV-13-A', $4)`,
		invoiceID, h.tenantA, entityA, batchID,
	); err != nil {
		if code := pgCode(err); code == "42P01" {
			t.Fatalf("seed invoices: undefined_table (42P01) — invoices migration not applied yet: %v", err)
		}
		t.Fatalf("seed invoice with import_batch_id: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM invoices WHERE id = $1`, invoiceID)
	}()

	if _, err := h.super.Exec(ctx, `DELETE FROM import_batches WHERE id = $1`, batchID); err != nil {
		t.Fatalf("delete parent import_batches row: %v", err)
	}

	if n := mustCount(t, h.super, `SELECT count(*) FROM invoices WHERE id = $1`, invoiceID); n != 1 {
		t.Fatalf("invoice rows after import_batch delete = %d, want 1 (invoice must survive)", n)
	}

	var importBatchID *string
	if err := h.super.QueryRow(ctx, `SELECT import_batch_id FROM invoices WHERE id = $1`, invoiceID).Scan(&importBatchID); err != nil {
		t.Fatalf("read back import_batch_id: %v", err)
	}
	if importBatchID != nil {
		t.Errorf("import_batch_id after batch delete = %q, want NULL (ON DELETE SET NULL)", *importBatchID)
	}
}

// INV-RLS-14: rule_set_version_id is a nullable FK to rule_set_versions. A
// non-existent version id is refused (23503 foreign_key_violation); a valid one (the
// M3-05-seeded active version) is accepted.
func TestRLS_InvoicesRuleSetVersionFK(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "INV-14 A Corp")
	defer cleanupEntityA()

	// A non-existent rule_set_versions id is refused.
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`INSERT INTO invoices (tenant_id, entity_id, invoice_number, rule_set_version_id) VALUES ($1, $2, 'INV-14-BOGUS', $3)`,
			h.tenantA, entityA, uuid.NewString(),
		)
		return e
	})
	if err == nil {
		t.Fatal("insert with a non-existent rule_set_version_id succeeded, want foreign_key_violation (SQLSTATE 23503)")
	}
	if code := pgCode(err); code != "23503" {
		t.Fatalf("insert with a non-existent rule_set_version_id: SQLSTATE = %q, want 23503 (foreign_key_violation): %v", code, err)
	}

	// A valid rule_set_versions id (an M3-04/M3-05 seeded row) is accepted.
	var versionID string
	if err := h.super.QueryRow(ctx, `SELECT id FROM rule_set_versions LIMIT 1`).Scan(&versionID); err != nil {
		t.Fatalf("look up a valid rule_set_versions id (is the M3-05 seed applied?): %v", err)
	}

	var invoiceID string
	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO invoices (tenant_id, entity_id, invoice_number, rule_set_version_id) VALUES ($1, $2, 'INV-14-VALID', $3) RETURNING id`,
			h.tenantA, entityA, versionID,
		).Scan(&invoiceID)
	})
	if err != nil {
		t.Fatalf("insert with a valid rule_set_version_id: want success, got: %v", err)
	}
	_, _ = h.super.Exec(context.Background(), `DELETE FROM invoices WHERE id = $1`, invoiceID)
}

// INV-RLS-15 (D8 cross-tenant dangling-ref, DOCUMENTING not defending): as tenant A,
// INSERT an invoice whose entity_id belongs to tenant B's business_entities row. The
// FK check bypasses RLS (Postgres referential-integrity triggers run with elevated
// internal privilege), and the row's own tenant_id = A passes the WITH CHECK — so
// this SUCCEEDS. This pins the accepted D8 residual: tenant-owned→tenant-owned FKs
// are plain per-column FKs, not composite same-tenant FKs (story QA-Verify
// disposition [2]). The second half proves it is not a READ leak: a join from the
// invoice to business_entities under A's RLS returns ZERO parent rows — B's entity
// row stays invisible to A, so the reference dangles from A's view rather than
// opening a window into B's data. If a future story adopts composite same-tenant
// FKs, this spec flips to expect 23503.
func TestRLS_InvoicesCrossTenantDanglingEntityRef(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityB, cleanupEntityB := seedBusinessEntity(t, h.tenantB, "INV-15 B Corp")
	defer cleanupEntityB()

	var invoiceID string
	err := db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO invoices (tenant_id, entity_id, invoice_number) VALUES ($1, $2, 'INV-15-DANGLING') RETURNING id`,
			h.tenantA, entityB,
		).Scan(&invoiceID)
	})
	if err != nil {
		t.Fatalf("insert invoice with cross-tenant entity_id (documenting D8 residual): want success, got: %v", err)
	}
	defer func() {
		_, _ = h.super.Exec(context.Background(), `DELETE FROM invoices WHERE id = $1`, invoiceID)
	}()

	err = db.WithinTenantTx(ctx, h.app, h.tenantA, func(tx pgx.Tx) error {
		n := mustCount(t, tx,
			`SELECT count(*) FROM invoices i JOIN business_entities b ON i.entity_id = b.id WHERE i.id = $1`,
			invoiceID,
		)
		if n != 0 {
			t.Errorf("join to cross-tenant parent entity under A's RLS = %d rows, want 0 (no read leak)", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTenantTx (join check): %v", err)
	}
}

// INV-RLS-16: entity_id is ON DELETE RESTRICT. Deleting a business_entities row that
// still has a live invoice is refused (23503) — a portfolio edit must not silently
// destroy a durable legal/fiscal record (D9).
func TestRLS_InvoicesEntityDeleteRestricted(t *testing.T) {
	h := requireHarness(t)
	ctx := context.Background()

	entityA, cleanupEntityA := seedBusinessEntity(t, h.tenantA, "INV-16 A Corp")
	invoiceID, cleanupInvoice := seedInvoice(t, h.tenantA, entityA, "INV-16-A")
	// Cleanup order matters: the invoice must be removed BEFORE the entity (entity_id
	// is ON DELETE RESTRICT, so cleaning up the entity first would recreate the very
	// violation under test). Deferred funcs run LIFO, so defer the entity cleanup
	// FIRST and the invoice cleanup SECOND — making the invoice cleanup run first.
	defer cleanupEntityA()
	defer cleanupInvoice()

	_, err := h.super.Exec(ctx, `DELETE FROM business_entities WHERE id = $1`, entityA)
	if err == nil {
		t.Fatal("delete parent business_entities row with a live invoice succeeded, want foreign_key_violation (SQLSTATE 23503, ON DELETE RESTRICT)")
	}
	if code := pgCode(err); code != "23503" {
		t.Fatalf("delete parent business_entities row with a live invoice: SQLSTATE = %q, want 23503 (foreign_key_violation): %v", code, err)
	}

	if n := mustCount(t, h.super, `SELECT count(*) FROM invoices WHERE id = $1`, invoiceID); n != 1 {
		t.Errorf("invoice rows after refused entity delete = %d, want 1 (row must survive)", n)
	}
}
